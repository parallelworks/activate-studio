package extract

import "testing"

// The key a library's owner lent the job describes its images, in place of
// the Studio's.
func TestVisionKeyFromTheJob(t *testing.T) {
	t.Setenv("PW_API_KEY", "studio-key")
	t.Setenv("ADE_VISION_MODEL", "caption-model")
	t.Setenv(VisionKeyEnv, "")
	if v := VisionFromEnv(); v.Key != "studio-key" || v.Model != "caption-model" {
		t.Fatalf("%+v", v)
	}
	t.Setenv(VisionKeyEnv, "owners-key")
	if v := VisionFromEnv(); v.Key != "owners-key" {
		t.Fatalf("%+v", v)
	}
}
