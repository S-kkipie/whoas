package app

import (
	"reflect"
	"testing"
)

func TestSetEnv(t *testing.T) {
	got := setEnv(
		[]string{"PATH=/a", "X=1", "PATH=/b", "WHOAS_PROFILE=p"},
		"PATH=/s:/a",
		"WHOAS_PROFILE=w",
	)
	want := []string{"X=1", "PATH=/s:/a", "WHOAS_PROFILE=w"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("setEnv() = %v, want %v", got, want)
	}
}
