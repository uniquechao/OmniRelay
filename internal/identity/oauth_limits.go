package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	oauthStartsPerSource        = 20
	oauthStartsPerInstance      = 120
	oauthUnverifiedPerInstance  = 240
	oauthCompletionsPerInstance = 240
	oauthClockSkew              = 30 * time.Second
	// The server shares a 12-connection pool with other operations.
	oauthVerifiedLookups = 8
	oauthLegacyLookups   = 2
	oauthReplayCacheSize = 4096
)

var (
	errOAuthRateLimited = errors.New("OAuth rate limit reached")
	errOAuthBusy        = errors.New("OAuth callback capacity busy")
)

type oauthWindow struct {
	started time.Time
	count   int
}

func (w *oauthWindow) reset(now time.Time) bool {
	if !w.started.IsZero() && now.Sub(w.started) < time.Minute {
		return false
	}
	*w = oauthWindow{started: now}
	return true
}

type oauthAttempt struct {
	expires time.Time
	pending bool
}

type oauthLimits struct {
	mu                               sync.Mutex
	starts, unverified, completions  oauthWindow
	sources                          map[netip.Addr]int
	attempts                         map[string]oauthAttempt
	nextCleanup                      time.Time
	verifiedInFlight, legacyInFlight int
}

func (l *oauthLimits) allowStart(now time.Time, source netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.starts.reset(now) {
		l.sources = make(map[netip.Addr]int)
	}
	if l.starts.count >= oauthStartsPerInstance || l.sources[source] >= oauthStartsPerSource {
		return false
	}
	l.sources[source]++
	l.starts.count++
	return true
}

func (l *oauthLimits) allowCompletion(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.completions.reset(now)
	if l.completions.count >= oauthCompletionsPerInstance {
		return false
	}
	l.completions.count++
	return true
}

func (l *oauthLimits) rejectUnverified(now time.Time) error {
	if err := l.countUnverified(now); err != nil {
		return err
	}
	return pgx.ErrNoRows
}

func (l *oauthLimits) countUnverified(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unverified.reset(now)
	if l.unverified.count >= oauthUnverifiedPerInstance {
		return errOAuthRateLimited
	}
	l.unverified.count++
	return nil
}

func (s *Service) oauthSource(r *http.Request) netip.Addr {
	tokens := r.Header.Values("X-Omnirelay-Proxy-Token")
	sources := r.Header.Values("X-Omnirelay-Client-IP")
	if r.TLS != nil && s.cfg.OAuthProxyToken != "" && len(tokens) == 1 && len(sources) == 1 &&
		hmac.Equal([]byte(tokens[0]), []byte(s.cfg.OAuthProxyToken)) {
		if source, err := netip.ParseAddr(sources[0]); err == nil && source.Zone() == "" {
			return source.Unmap()
		}
	}
	peer, _ := netip.ParseAddrPort(r.RemoteAddr)
	return peer.Addr().WithZone("").Unmap()
}

// Keep the wire format compatible with older replicas: the state is still a
// 32-byte opaque value, and the same state and browser digests are stored.
func (s *Service) newOAuthState(now time.Time, browser string) string {
	var state [32]byte
	binary.BigEndian.PutUint64(state[:8], uint64(now.Unix()))
	copy(state[8:], s.oauthStateMAC(state[:8], browser))
	return base64.RawURLEncoding.EncodeToString(state[:])
}

func (s *Service) oauthStateMAC(issued []byte, browser string) []byte {
	mac := hmac.New(sha256.New, []byte(s.cfg.AppSecret))
	mac.Write([]byte("OmniRelay OAuth state v1\x00" + s.cfg.TenantKey + "\x00" + s.cfg.AppID + "\x00" + s.callback + "\x00"))
	mac.Write(issued)
	mac.Write([]byte(browser))
	return mac.Sum(nil)[:24]
}

func (s *Service) admitCallback(ctx context.Context, now time.Time, state, browser string) (string, string, error) {
	if !isSecret(state) {
		return "", "", s.oauth.rejectUnverified(now)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(state)
	if s.cfg.AppSecret == "" || !hmac.Equal(raw[8:], s.oauthStateMAC(raw[:8], browser)) {
		// A pre-upgrade or pre-rotation transaction can still be proved by PostgreSQL.
		if err := s.oauth.countUnverified(now); err != nil {
			return "", "", err
		}
		if !s.oauth.beginLegacy() {
			return "", "", errOAuthBusy
		}
		defer s.oauth.finishLegacy()
		return s.consumeLogin(ctx, state, browser)
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(raw[:8])), 0)
	expires := issued.Add(loginTTL + oauthClockSkew)
	if issued.After(now.Add(oauthClockSkew)) || !now.Before(expires) {
		return "", "", s.oauth.rejectUnverified(now)
	}
	if err := s.oauth.beginVerified(now, expires, state); err != nil {
		return "", "", err
	}
	verifier, target, err := s.consumeLogin(ctx, state, browser)
	s.oauth.finishVerified(state, err == nil || errors.Is(err, pgx.ErrNoRows))
	return verifier, target, err
}

func (l *oauthLimits) beginLegacy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.legacyInFlight >= oauthLegacyLookups {
		return false
	}
	l.legacyInFlight++
	return true
}

func (l *oauthLimits) finishLegacy() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.legacyInFlight--
}

func (l *oauthLimits) beginVerified(now, expires time.Time, state string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]oauthAttempt)
	}
	if !now.Before(l.nextCleanup) {
		for key, attempt := range l.attempts {
			if !attempt.pending && !now.Before(attempt.expires) {
				delete(l.attempts, key)
			}
		}
		l.nextCleanup = now.Add(time.Second)
	}
	if _, exists := l.attempts[state]; exists {
		return pgx.ErrNoRows
	}
	if l.verifiedInFlight >= oauthVerifiedLookups || len(l.attempts) >= oauthReplayCacheSize {
		return errOAuthBusy
	}
	l.attempts[state] = oauthAttempt{expires: expires, pending: true}
	l.verifiedInFlight++
	return nil
}

func (l *oauthLimits) finishVerified(state string, spent bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.verifiedInFlight--
	if !spent {
		delete(l.attempts, state)
		return
	}
	attempt := l.attempts[state]
	attempt.pending = false
	l.attempts[state] = attempt
}

func oauthRetry(w http.ResponseWriter, after time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(after/time.Second)))
	writeFailure(w, http.StatusTooManyRequests, "too_many_requests")
}
