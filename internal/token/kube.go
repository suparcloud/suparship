package token

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// SecretNamespace and SecretName locate the K8s Secret holding every project's
// API tokens. One Secret keyed by token id keeps validation O(1): the bearer
// token carries its own id, so Resolve reads a single data entry without
// scanning. Project name lives in each record's metadata for listing/scoping.
const (
	SecretNamespace = "suparship-system"
	SecretName      = "suparship-api-tokens"
)

// KubeStore persists API token records as entries in a single K8s Secret.
type KubeStore struct {
	client kubernetes.Interface
	ns     string
	name   string
}

// NewKubeStore returns a KubeStore backed by the default Secret.
func NewKubeStore(client kubernetes.Interface) *KubeStore {
	return &KubeStore{client: client, ns: SecretNamespace, name: SecretName}
}

func (s *KubeStore) secrets() corev1client.SecretInterface {
	return s.client.CoreV1().Secrets(s.ns)
}

func (s *KubeStore) Issue(ctx context.Context, meta Metadata) (string, Metadata, error) {
	id, secret, plaintext, err := generate()
	if err != nil {
		return "", Metadata{}, err
	}
	meta.ID = id
	rec := record{Metadata: meta, SecretHash: hashSecret(secret)}
	blob, err := json.Marshal(rec)
	if err != nil {
		return "", Metadata{}, fmt.Errorf("marshalling token record: %w", err)
	}

	sec, err := s.secrets().Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.name,
				Namespace: s.ns,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "suparship"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{id: blob},
		}
		if _, err = s.secrets().Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return "", Metadata{}, fmt.Errorf("creating token secret: %w", err)
		}
		return plaintext, meta, nil
	}
	if err != nil {
		return "", Metadata{}, fmt.Errorf("reading token secret: %w", err)
	}

	if sec.Data == nil {
		sec.Data = make(map[string][]byte)
	}
	sec.Data[id] = blob
	if _, err = s.secrets().Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return "", Metadata{}, fmt.Errorf("updating token secret: %w", err)
	}
	return plaintext, meta, nil
}

func (s *KubeStore) Resolve(ctx context.Context, plaintext string) (Metadata, error) {
	id, secret, ok := parse(plaintext)
	if !ok {
		return Metadata{}, ErrInvalid
	}
	sec, err := s.secrets().Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Metadata{}, ErrInvalid
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("reading token secret: %w", err)
	}
	rec, err := decodeRecord(sec.Data[id])
	if err != nil || !secretMatches(secret, rec.SecretHash) {
		return Metadata{}, ErrInvalid
	}
	if rec.Expired(time.Now()) {
		return Metadata{}, ErrExpired
	}
	return rec.Metadata, nil
}

func (s *KubeStore) List(ctx context.Context, project string) ([]Metadata, error) {
	sec, err := s.secrets().Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return []Metadata{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading token secret: %w", err)
	}
	out := make([]Metadata, 0, len(sec.Data))
	for _, blob := range sec.Data {
		rec, err := decodeRecord(blob)
		if err != nil {
			continue // skip corrupt entries rather than failing the whole list
		}
		if rec.Project == project {
			out = append(out, rec.Metadata)
		}
	}
	return out, nil
}

func (s *KubeStore) Delete(ctx context.Context, project, id string) error {
	sec, err := s.secrets().Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("reading token secret: %w", err)
	}
	rec, err := decodeRecord(sec.Data[id])
	if err != nil || rec.Project != project {
		return ErrNotFound
	}
	delete(sec.Data, id)
	if _, err = s.secrets().Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating token secret: %w", err)
	}
	return nil
}

// decodeRecord unmarshals a stored record blob. A nil/empty blob (missing key)
// yields an error so callers treat it as "not found".
func decodeRecord(blob []byte) (record, error) {
	if len(blob) == 0 {
		return record{}, ErrInvalid
	}
	var rec record
	if err := json.Unmarshal(blob, &rec); err != nil {
		return record{}, err
	}
	return rec, nil
}
