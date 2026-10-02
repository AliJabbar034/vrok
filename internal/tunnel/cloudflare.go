package tunnel

import (
	"net/url"
	"regexp"
)

func init() { Register("cloudflare", newCloudflare) }

// newCloudflare drives `cloudflared tunnel --url`, which issues a free
// try-cloudflare hostname with no account and no configuration.
func newCloudflare(cfg Config) (Tunnel, error) {
	return newProcessTunnel(cloudflareSpec(), cfg), nil
}

// cloudflareSpec describes how to drive cloudflared. The auto provider reuses
// it with a different way of locating the binary, so it is built here rather
// than inlined.
func cloudflareSpec() processSpec {
	return processSpec{
		provider: "cloudflare",
		binary:   "cloudflared",
		hint:     "Install it with `brew install cloudflared` or from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
		args: func(target *url.URL) []string {
			return []string{
				"tunnel",
				"--no-autoupdate",
				// cloudflared's own logs are vrok's only source for the
				// assigned hostname, so they have to be on and parseable.
				"--loglevel", "info",
				"--url", target.String(),
			}
		},
		urlPatterns: []*regexp.Regexp{
			regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com`),
		},
	}
}
