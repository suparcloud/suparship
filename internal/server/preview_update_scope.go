package server

import "reflect"

// Preview-band overlays do not change stable or validation inputs. Keep these
// saves out of stable publication and its validation lifecycle lease. Unknown or
// mixed request fields conservatively retain the full publication path.
func previewValuesOnly(req updateAppRequest) bool {
	if len(req.EnvRawValues)+len(req.EnvComponentValues)+len(req.EnvComponentEnvVars) == 0 {
		return false
	}
	for env := range req.EnvRawValues {
		if env != "preview" {
			return false
		}
	}
	for env := range req.EnvComponentValues {
		if env != "preview" {
			return false
		}
	}
	for env := range req.EnvComponentEnvVars {
		if env != "preview" {
			return false
		}
	}
	req.EnvRawValues = nil
	req.EnvComponentValues = nil
	req.EnvComponentEnvVars = nil
	return reflect.DeepEqual(req, updateAppRequest{})
}
