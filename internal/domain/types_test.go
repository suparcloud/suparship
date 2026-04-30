package domain

import "testing"

func TestEnvironment_EffectiveClusterRef(t *testing.T) {
	tests := []struct {
		name string
		env  Environment
		want string
	}{
		{
			name: "active set wins",
			env:  Environment{ClusterRefs: []string{"a", "b"}, ActiveClusterRef: "b"},
			want: "b",
		},
		{
			name: "active empty falls back to first",
			env:  Environment{ClusterRefs: []string{"a", "b"}},
			want: "a",
		},
		{
			name: "no clusters registered",
			env:  Environment{},
			want: "",
		},
		{
			name: "single cluster, no active",
			env:  Environment{ClusterRefs: []string{"only"}},
			want: "only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.EffectiveClusterRef(); got != tt.want {
				t.Errorf("EffectiveClusterRef() = %q, want %q", got, tt.want)
			}
		})
	}
}
