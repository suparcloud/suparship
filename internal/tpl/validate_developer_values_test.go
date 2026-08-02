package tpl

import "testing"

func f64(v float64) *float64 { return &v }

func TestValidateDeveloperValues(t *testing.T) {
	cases := []struct {
		name    string
		fields  []ValueField
		wantErr string // substring; "" = expect success
	}{
		{
			name:    "no projection declared",
			fields:  nil,
			wantErr: "",
		},
		{
			name:    "valid minimal entry",
			fields:  []ValueField{{Path: "image.repository"}},
			wantErr: "",
		},
		{
			// Type is optional here (unlike Input): an untyped field is a valid
			// free-form passthrough the YAML editor renders fine.
			name: "valid fully-specified entries",
			fields: []ValueField{
				{Path: "components.web.image.repository", Title: "Image", Type: InputTypeString, Required: true},
				{Path: "components.web.port", Title: "Port", Type: InputTypeNumber, Min: f64(1), Max: f64(65535)},
				{Path: "size", Type: InputTypeEnum, Options: []string{"small", "large"}},
				{Path: "name", Type: InputTypeString, Pattern: "^[a-z]+$"},
			},
			wantErr: "",
		},
		{
			name:    "missing path",
			fields:  []ValueField{{Title: "No path"}},
			wantErr: "path is required",
		},
		{
			// Two entries for one path would seed the key twice.
			name: "duplicate path",
			fields: []ValueField{
				{Path: "image.repository"},
				{Path: "image.repository"},
			},
			wantErr: "duplicate path",
		},
		{
			name:    "unsupported type",
			fields:  []ValueField{{Path: "a", Type: InputType("object")}},
			wantErr: "unsupported type",
		},
		{
			name:    "enum without options",
			fields:  []ValueField{{Path: "size", Type: InputTypeEnum}},
			wantErr: "enum type requires at least one option",
		},
		{
			name:    "min exceeds max",
			fields:  []ValueField{{Path: "port", Type: InputTypeNumber, Min: f64(10), Max: f64(1)}},
			wantErr: "must not exceed max",
		},
		{
			name:    "bad pattern regex",
			fields:  []ValueField{{Path: "name", Pattern: "["}},
			wantErr: "invalid pattern",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := validTemplate()
			tmpl.Spec.DeveloperValues = tc.fields
			err := tmpl.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			mustContain(t, err, tc.wantErr)
		})
	}
}

// TestDeveloperValuesRoundTrip pins that the projection survives the YAML
// round-trip the sync engine performs (Parse → Marshal → Parse), since a template
// body is re-marshalled into its ConfigMap on every sync.
func TestDeveloperValuesRoundTrip(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.DeveloperValues = []ValueField{{
		Path: "components.web.resources.size", Title: "Size", Type: InputTypeEnum,
		Options: []string{"small", "large"}, Description: "Resource profile", Default: "small",
	}}
	data, err := Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := back.Spec.DeveloperValues
	if len(got) != 1 {
		t.Fatalf("developerValues lost in round-trip: %+v", got)
	}
	if got[0].Path != "components.web.resources.size" || got[0].Type != InputTypeEnum ||
		got[0].Default != "small" || len(got[0].Options) != 2 {
		t.Errorf("round-trip altered the field: %+v", got[0])
	}
}
