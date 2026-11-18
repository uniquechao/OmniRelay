package feishu

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/uniquechao/OmniRelay/internal/system"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordination "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const BotLeaseName = "omnirelay-feishu-bot"

var errLeaseLost = errors.New("Feishu Bot Lease ownership unavailable")

type Lease struct {
	client    coordination.LeasesGetter
	namespace string
	identity  string
	duration  time.Duration
	deadline  time.Duration
	retry     time.Duration
}

func NewLease(namespace, podUID string) (*Lease, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, errors.New("Feishu Bot Lease requires Kubernetes in-cluster credentials")
	}
	cfg.Timeout = 2 * time.Second
	client, err := coordination.NewForConfig(cfg)
	if err != nil {
		return nil, errors.New("could not initialize Feishu Bot Lease client")
	}
	return &Lease{
		client: client, namespace: namespace, identity: podUID + "-" + rand.Text(),
		duration: 30 * time.Second, deadline: 8 * time.Second, retry: 2 * time.Second,
	}, nil
}

// Run holds the pre-created Lease until the connection and its database fence
// have been closed. On involuntary loss a different holder may already own it.
func (l *Lease) Run(ctx context.Context, own func(context.Context, func(context.Context) error) error) {
	for ctx.Err() == nil {
		err := l.elect(ctx, own)
		if errors.Is(err, system.ErrStaleGeneration) {
			return
		}
		sleepContext(ctx, l.retry)
	}
}

func (l *Lease) elect(ctx context.Context, own func(context.Context, func(context.Context) error) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lock := &existingLease{LeaseLock: &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{Namespace: l.namespace, Name: BotLeaseName},
		Client:    l.client, LockConfig: resourcelock.ResourceLockConfig{Identity: l.identity},
	}}
	done := make(chan error, 1)
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: lock, LeaseDuration: l.duration, RenewDeadline: l.deadline, RetryPeriod: l.retry,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leader context.Context) {
				err := own(leader, l.verify)
				cancel()
				done <- err
			},
			OnStoppedLeading: func() {},
		},
	})
	if err != nil {
		return errLeaseLost
	}
	elector.Run(ctx)
	if !lock.acquired {
		return ctx.Err()
	}
	err = <-done
	l.release()
	return err
}

func (l *Lease) verify(ctx context.Context) error {
	lease, err := l.client.Leases(l.namespace).Get(ctx, BotLeaseName, metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.identity {
		return errLeaseLost
	}
	return nil
}

func (l *Lease) release() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	leases := l.client.Leases(l.namespace)
	lease, err := leases.Get(ctx, BotLeaseName, metav1.GetOptions{})
	if err != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.identity {
		return
	}
	empty := ""
	lease.Spec.HolderIdentity = &empty
	// Update includes resourceVersion. A concurrent election wins over release.
	_, _ = leases.Update(ctx, lease, metav1.UpdateOptions{})
}

type existingLease struct {
	*resourcelock.LeaseLock
	acquired bool
}

func (*existingLease) Create(context.Context, resourcelock.LeaderElectionRecord) error {
	return errors.New("Feishu Bot Lease must be pre-created")
}

func (l *existingLease) Update(ctx context.Context, record resourcelock.LeaderElectionRecord) error {
	if err := l.LeaseLock.Update(ctx, record); err != nil {
		return err
	}
	l.acquired = true
	return nil
}
