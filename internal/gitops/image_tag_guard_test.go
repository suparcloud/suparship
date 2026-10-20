package gitops

import "testing"

func TestClearedImageTag(t *testing.T) {
	image := func(repo, tag string) map[string]any {
		return map[string]any{"image": map[string]any{"repository": repo, "tag": tag}}
	}
	old := image("app", "release123")
	for _, tc := range []struct{ name, repo, tag, want string }{
		{"empty", "app", "", "image.tag"},
		{"whitespace", "app", " ", "image.tag"},
		{"explicit release", "app", "release456", ""},
		{"different repository", "other", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clearedImageTag(old, image(tc.repo, tc.tag), ""); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := clearedImageTag(map[string]any{"worker": old}, map[string]any{"worker": image("app", "")}, ""); got != "worker.image.tag" {
		t.Fatal(got)
	}
	if got := clearedImageTag(nil, image("app", ""), ""); got != "" {
		t.Fatal(got)
	}
}
