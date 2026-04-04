package daemon

var version string

func SetVersion(v string) {
	version = v
}

func IsDevMode() bool {
	return version == "dev" || version == ""
}

func SignalingURL() string {
	if IsDevMode() {
		return "ws://localhost:8787/ws/connect"
	}
	return "wss://carryon.dev/ws/connect"
}

func APIBaseURL() string {
	if IsDevMode() {
		return "http://localhost:8787"
	}
	return "https://carryon.dev"
}
