package main

import "testing"

func TestValidPluginName(t *testing.T) {
	for _, name := range []string{"scrape.spotify", "tool_ffmpeg", "my-plugin-2", "Plugin.v1"} {
		if !validPluginName(name) {
			t.Errorf("validPluginName(%q)=false, want true", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../outside", "../../config", "a/b", `a\\b`, "C:outside", ".hidden"} {
		if validPluginName(name) {
			t.Errorf("validPluginName(%q)=true, want false", name)
		}
	}
}

func TestValidPluginRelativePath(t *testing.T) {
	for _, name := range []string{"bin/plugin", "ui/index.html", "plugin.exe"} {
		if !validPluginRelativePath(name) {
			t.Errorf("validPluginRelativePath(%q)=false, want true", name)
		}
	}
	for _, name := range []string{"", ".", "../outside", "foo/../outside", "../../etc/passwd", "/etc/passwd", `..\\outside`, `C:\\Windows\\system.ini`, "C:/Windows/system.ini"} {
		if validPluginRelativePath(name) {
			t.Errorf("validPluginRelativePath(%q)=true, want false", name)
		}
	}
}
