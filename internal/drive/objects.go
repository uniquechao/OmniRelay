package drive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const originalLimit = 64 << 20

var errOverLimit = errors.New("超过同步大小上限")

type ObjectStore interface {
	Discard(context.Context, string) error
	Put(context.Context, string, io.ReadSeeker, int64) error
	Open(context.Context, string, int64) (io.ReadSeekCloser, error)
}

type privateBucket struct {
	client *minio.Client
	bucket string
}

func ConfiguredObjects() (ObjectStore, error) {
	endpoint, bucket := os.Getenv("OMNIRELAY_S3_ENDPOINT"), os.Getenv("OMNIRELAY_S3_BUCKET")
	access, secret := os.Getenv("OMNIRELAY_S3_ACCESS_KEY_ID"), os.Getenv("OMNIRELAY_S3_SECRET_ACCESS_KEY")
	if endpoint == "" && bucket == "" && access == "" && secret == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || bucket == "" || access == "" || secret == "" {
		return nil, errors.New("文件对象存储配置不完整")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ca := os.Getenv("OMNIRELAY_S3_CA_FILE"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("对象存储证书无效")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: true, Transport: transport, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return nil, err
	}
	return &privateBucket{client, bucket}, nil
}

func (b *privateBucket) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	info, err := b.client.StatObject(ctx, b.bucket, key, minio.StatObjectOptions{})
	if err == nil && info.Size == size && info.Metadata.Get("X-Amz-Meta-Sha256") == path.Base(key) {
		return nil
	}
	if err != nil && minio.ToErrorResponse(err).StatusCode != http.StatusNotFound {
		return err
	}
	_, err = b.client.PutObject(ctx, b.bucket, key, body, size, minio.PutObjectOptions{ContentType: "application/octet-stream", SendContentMd5: true, UserMetadata: map[string]string{"sha256": path.Base(key)}})
	if err != nil {
		return err
	}
	info, err = b.client.StatObject(ctx, b.bucket, key, minio.StatObjectOptions{})
	if err == nil && (info.Size != size || info.Metadata.Get("X-Amz-Meta-Sha256") != path.Base(key)) {
		return ErrPartial
	}
	return err
}

func (b *privateBucket) Open(ctx context.Context, key string, size int64) (io.ReadSeekCloser, error) {
	obj, err := b.client.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	stat, err := obj.Stat()
	if err == nil && (stat.Size != size || stat.Metadata.Get("X-Amz-Meta-Sha256") != path.Base(key)) {
		err = ErrPartial
	}
	if err != nil {
		obj.Close()
		return nil, err
	}
	return obj, nil
}

type storedObject struct {
	key, mime string
	size      int64
	digest    []byte
}

// 先在有上限的私有临时文件内计算摘要；重试和并发发布都使用同一个租户内对象键。
func (s *Service) storeObject(ctx context.Context, body io.Reader, declared *int64) (storedObject, error) {
	var obj storedObject
	if s.objects == nil {
		return obj, ErrUnavailable
	}
	if declared != nil && *declared > originalLimit {
		return obj, errOverLimit
	}
	file, err := os.CreateTemp("", "omnirelay-original-*")
	if err != nil {
		return obj, err
	}
	// 容器重启也不会留下未发布的下载文件。
	if err := os.Remove(file.Name()); err != nil {
		defer os.Remove(file.Name())
	}
	defer file.Close()
	h := sha256.New()
	obj.size, err = io.Copy(io.MultiWriter(file, h), io.LimitReader(body, originalLimit+1))
	if err != nil {
		return obj, ErrPartial
	}
	if obj.size > originalLimit {
		return obj, errOverLimit
	}
	if declared != nil && *declared != obj.size {
		return obj, ErrPartial
	}
	var prefix [512]byte
	n, err := file.ReadAt(prefix[:], 0)
	if err != nil && err != io.EOF {
		return obj, err
	}
	obj.mime = http.DetectContentType(prefix[:n])
	obj.digest = h.Sum(nil)
	namespace := sha256.Sum256([]byte(s.tenant))
	obj.key = "sync-objects/" + hex.EncodeToString(namespace[:]) + "/" + hex.EncodeToString(obj.digest)
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return obj, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO feishu_sync_objects(tenant_key,object_key) VALUES($1,$2)
    ON CONFLICT(tenant_key,object_key) DO UPDATE SET reserved_until=EXCLUDED.reserved_until WHERE NOT feishu_sync_objects.published`, s.tenant, obj.key)
	if err != nil {
		return obj, err
	}
	return obj, s.objects.Put(ctx, obj.key, file, obj.size)
}

func (b *privateBucket) Discard(ctx context.Context, key string) error {
	return b.client.RemoveObject(ctx, b.bucket, key, minio.RemoveObjectOptions{})
}

// 回收期间只锁定一条过期待发布记录。相同内容的新领取必须等删除完成才能重新上传。
func (s *Service) discardAbandoned(ctx context.Context) error {
	if s.objects == nil {
		return nil
	}
	limited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return pgx.BeginFunc(limited, s.pool, func(tx pgx.Tx) error {
		var key string
		err := tx.QueryRow(limited, `SELECT object_key FROM feishu_sync_objects WHERE tenant_key=$1
        AND NOT published AND reserved_until<now() ORDER BY reserved_until LIMIT 1 FOR UPDATE SKIP LOCKED`, s.tenant).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = s.objects.Discard(limited, key); err != nil {
			return err
		}
		_, err = tx.Exec(limited, `DELETE FROM feishu_sync_objects WHERE tenant_key=$1 AND object_key=$2`, s.tenant, key)
		return err
	})
}

// SyncedMessageVideo opens only the current original of the live-validated message share.
func (s *Service) SyncedMessageVideo(ctx context.Context, ref Ref) (io.ReadSeekCloser, error) {
	if s.objects == nil {
		return nil, nil
	}
	var key string
	var size int64
	err := s.pool.QueryRow(ctx, `SELECT v.object_key,v.byte_size FROM feishu_file_shares sh
        JOIN feishu_files f ON (f.id,f.tenant_key)=(sh.file_id,sh.tenant_key)
        JOIN feishu_file_versions v ON (v.file_id,v.id)=(sh.file_id,sh.version_id)
        WHERE sh.tenant_key=$1 AND sh.app_id=$2 AND sh.message_id=$3 AND f.kind=$4 AND f.token=$5
          AND sh.present AND sh.state='complete' AND v.object_key IS NOT NULL`,
		s.tenant, s.app, ref.MessageID, ref.Kind, ref.Token).Scan(&key, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.objects.Open(ctx, key, size)
}

// Unsynced uploads use private, bounded scratch space so seeking does not buffer the movie in Go memory.
func SeekableMessageVideo(snap Snapshot) (io.ReadSeekCloser, error) {
	var src io.Reader = bytes.NewReader(snap.Content)
	if snap.Stream != nil {
		defer snap.Stream.Close()
		src = snap.Stream
	}
	f, err := os.CreateTemp("", "omnirelay-chat-video-*")
	if err != nil {
		return nil, err
	}
	if err = os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	size, err := io.Copy(f, io.LimitReader(src, originalLimit+1))
	if err != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	if size > originalLimit {
		f.Close()
		return nil, ErrPartial
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
