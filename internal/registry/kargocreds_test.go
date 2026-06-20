package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func seedAuthSecret(t *testing.T, s *Store, name string, data map[string][]byte) {
	t.Helper()
	_, err := s.client.CoreV1().Secrets(namespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed auth secret: %v", err)
	}
}

func TestEnsureKargoCred_PasswordKey(t *testing.T) {
	s := NewStore(fake.NewSimpleClientset())
	if err := s.Save(context.Background(), &Config{
		Enabled: true, URL: "acr.example.com", Username: "acruser", AuthSecretRef: "reg-creds",
	}); err != nil {
		t.Fatal(err)
	}
	seedAuthSecret(t, s, "reg-creds", map[string][]byte{"password": []byte("s3cret")})

	if err := s.EnsureKargoCred(context.Background(), "voiceai"); err != nil {
		t.Fatalf("EnsureKargoCred: %v", err)
	}

	sec, err := s.client.CoreV1().Secrets("voiceai").Get(context.Background(), KargoCredSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get kargo cred secret: %v", err)
	}
	if sec.Labels[kargoCredTypeLabel] != kargoCredTypeImage {
		t.Errorf("cred-type label = %q, want %q", sec.Labels[kargoCredTypeLabel], kargoCredTypeImage)
	}
	if string(sec.Data["username"]) != "acruser" || string(sec.Data["password"]) != "s3cret" {
		t.Errorf("secret creds = %s/%s, want acruser/s3cret", sec.Data["username"], sec.Data["password"])
	}
	// repoURL is a regex (repoURLIsRegex=true) matching the host and every repo
	// under it, so it matches a full subscription path.
	if string(sec.Data["repoURLIsRegex"]) != "true" {
		t.Errorf("repoURLIsRegex = %q, want true", sec.Data["repoURLIsRegex"])
	}
	pattern := string(sec.Data["repoURL"])
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("repoURL %q is not a valid regex: %v", pattern, err)
	}
	if !re.MatchString("acr.example.com/biglysales-voiceai-livekit") {
		t.Errorf("repoURL regex %q does not match a full repo path", pattern)
	}
	if re.MatchString("evil-acr.example.com.attacker.io/x") {
		t.Errorf("repoURL regex %q must not match a different registry host", pattern)
	}
}

func TestEnsureKargoCred_DockerConfigJSON(t *testing.T) {
	s := NewStore(fake.NewSimpleClientset())
	if err := s.Save(context.Background(), &Config{
		Enabled: true, URL: "acr.example.com", AuthSecretRef: "reg-creds",
	}); err != nil {
		t.Fatal(err)
	}
	dcj, _ := json.Marshal(map[string]any{
		"auths": map[string]any{
			"acr.example.com": map[string]any{
				"auth": base64.StdEncoding.EncodeToString([]byte("dcjuser:dcjpass")),
			},
		},
	})
	seedAuthSecret(t, s, "reg-creds", map[string][]byte{corev1.DockerConfigJsonKey: dcj})

	if err := s.EnsureKargoCred(context.Background(), "voiceai"); err != nil {
		t.Fatalf("EnsureKargoCred: %v", err)
	}
	sec, _ := s.client.CoreV1().Secrets("voiceai").Get(context.Background(), KargoCredSecretName, metav1.GetOptions{})
	if string(sec.Data["username"]) != "dcjuser" || string(sec.Data["password"]) != "dcjpass" {
		t.Errorf("dockerconfig creds = %s/%s, want dcjuser/dcjpass", sec.Data["username"], sec.Data["password"])
	}
}

func TestEnsureKargoCred_DisabledIsNoop(t *testing.T) {
	s := NewStore(fake.NewSimpleClientset())
	if err := s.Save(context.Background(), &Config{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKargoCred(context.Background(), "voiceai"); err != nil {
		t.Fatalf("EnsureKargoCred: %v", err)
	}
	_, err := s.client.CoreV1().Secrets("voiceai").Get(context.Background(), KargoCredSecretName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected no kargo cred secret when registry disabled, got err=%v", err)
	}
}

func TestEnsureKargoCred_Idempotent(t *testing.T) {
	s := NewStore(fake.NewSimpleClientset())
	_ = s.Save(context.Background(), &Config{Enabled: true, URL: "acr.example.com", Username: "u", AuthSecretRef: "reg-creds"})
	seedAuthSecret(t, s, "reg-creds", map[string][]byte{"password": []byte("p1")})
	if err := s.EnsureKargoCred(context.Background(), "voiceai"); err != nil {
		t.Fatal(err)
	}
	// Rotate the password and re-run: the secret updates in place.
	_ = s.client.CoreV1().Secrets(namespace).Delete(context.Background(), "reg-creds", metav1.DeleteOptions{})
	seedAuthSecret(t, s, "reg-creds", map[string][]byte{"password": []byte("p2")})
	if err := s.EnsureKargoCred(context.Background(), "voiceai"); err != nil {
		t.Fatal(err)
	}
	sec, _ := s.client.CoreV1().Secrets("voiceai").Get(context.Background(), KargoCredSecretName, metav1.GetOptions{})
	if string(sec.Data["password"]) != "p2" {
		t.Errorf("password = %q, want p2 (refreshed)", sec.Data["password"])
	}
}
