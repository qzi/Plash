package outboundgroup

import (
	"github.com/Dreamacro/clash/tunnel"
	"regexp"
	"time"

	C "github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/constant/provider"
)

const (
	defaultGetProxiesDuration = time.Second * 5
)

func getProvidersProxies(providers []provider.ProxyProvider, touch bool, filter string) []C.Proxy {
	proxies := []C.Proxy{}
	for _, provider := range providers {
		if touch {
			proxies = append(proxies, provider.ProxiesWithTouch()...)
		} else {
			proxies = append(proxies, provider.Proxies()...)
		}
	}

	var filterReg *regexp.Regexp = nil
	matchedProxies := []C.Proxy{}
	if len(filter) > 0 {
		filterReg = regexp.MustCompile(filter)
		for _, p := range proxies {
			if filterReg.MatchString(p.Name()) {
				matchedProxies = append(matchedProxies, p)
			}
		}

		if len(matchedProxies) > 0 {
			return matchedProxies
		} else {
			return append([]C.Proxy{}, tunnel.Proxies()["COMPATIBLE"])
		}
	} else {
		if len(proxies) == 0 {
			return append(proxies, tunnel.Proxies()["COMPATIBLE"])
		} else {
			return proxies
		}
	}

}
