package executor

import (
	"fmt"

	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/Dreamacro/clash/listener/tproxy"

	"github.com/Dreamacro/clash/adapter"
	"github.com/Dreamacro/clash/adapter/outboundgroup"
	"github.com/Dreamacro/clash/component/auth"
	"github.com/Dreamacro/clash/component/dialer"
	G "github.com/Dreamacro/clash/component/geodata"
	"github.com/Dreamacro/clash/component/iface"
	"github.com/Dreamacro/clash/component/profile"
	"github.com/Dreamacro/clash/component/profile/cachefile"
	"github.com/Dreamacro/clash/component/resolver"
	"github.com/Dreamacro/clash/component/trie"
	"github.com/Dreamacro/clash/config"
	C "github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/constant/provider"
	"github.com/Dreamacro/clash/dns"
	P "github.com/Dreamacro/clash/listener"
	authStore "github.com/Dreamacro/clash/listener/auth"
	"github.com/Dreamacro/clash/listener/tun/dev"
	"github.com/Dreamacro/clash/log"
	"github.com/Dreamacro/clash/tunnel"
)

var mux sync.Mutex

func readConfig(path string) ([]byte, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("configuration file %s is empty", path)
	}

	return data, err
}

// Parse config with default config path
func Parse() (*config.Config, error) {
	return ParseWithPath(C.Path.Config())
}

// ParseWithPath parse config with custom config path
func ParseWithPath(path string) (*config.Config, error) {
	buf, err := readConfig(path)
	if err != nil {
		return nil, err
	}

	return ParseWithBytes(buf)
}

// ParseWithBytes config with buffer
func ParseWithBytes(buf []byte) (*config.Config, error) {
	return config.Parse(buf)
}

// ApplyConfig dispatch configure to all parts
func ApplyConfig(cfg *config.Config, force bool) {
	mux.Lock()
	defer mux.Unlock()

	updateUsers(cfg.Users)
	updateHosts(cfg.Hosts)
	updateProxies(cfg.Proxies, cfg.Providers)
	updateRules(cfg.Rules, cfg.RuleProviders)
	updateIPTables(cfg.DNS, cfg.General, cfg.Tun)
	updateDNS(cfg.DNS, cfg.Tun)
	updateGeneral(cfg.General, cfg.Tun, force)
	updateTun(cfg.Tun)
	updateExperimental(cfg)
	loadProvider(cfg.RuleProviders, cfg.Providers)
	updateProfile(cfg)

}

func GetGeneral() *config.General {
	ports := P.GetPorts()
	authenticator := []string{}
	if auth := authStore.Authenticator(); auth != nil {
		authenticator = auth.Users()
	}

	general := &config.General{
		Inbound: config.Inbound{
			Port:           ports.Port,
			SocksPort:      ports.SocksPort,
			RedirPort:      ports.RedirPort,
			TProxyPort:     ports.TProxyPort,
			MixedPort:      ports.MixedPort,
			Authentication: authenticator,
			AllowLan:       P.AllowLan(),
			BindAddress:    P.BindAddress(),
		},
		Mode:          tunnel.Mode(),
		LogLevel:      log.Level(),
		IPv6:          !resolver.DisableIPv6,
		GeodataLoader: G.LoaderName(),
	}

	return general
}

func updateExperimental(c *config.Config) {}

func updateDNS(c *config.DNS, Tun *config.Tun) {
	if !c.Enable && !Tun.Enable {
		resolver.DefaultResolver = nil
		resolver.MainResolver = nil
		resolver.DefaultHostMapper = nil
		dns.ReCreateServer("", nil, nil)
		return
	}

	cfg := dns.Config{
		Main:         c.NameServer,
		Fallback:     c.Fallback,
		IPv6:         c.IPv6,
		EnhancedMode: c.EnhancedMode,
		Pool:         c.FakeIPRange,
		Hosts:        c.Hosts,
		FallbackFilter: dns.FallbackFilter{
			GeoIP:     c.FallbackFilter.GeoIP,
			GeoIPCode: c.FallbackFilter.GeoIPCode,
			IPCIDR:    c.FallbackFilter.IPCIDR,
			Domain:    c.FallbackFilter.Domain,
			GeoSite:   c.FallbackFilter.GeoSite,
		},
		Default: c.DefaultNameserver,
		Policy:  c.NameServerPolicy,
	}

	r := dns.NewResolver(cfg)
	mr := dns.NewMainResolver(r)
	m := dns.NewEnhancer(cfg)

	// reuse cache of old host mapper
	if old := resolver.DefaultHostMapper; old != nil {
		m.PatchFrom(old.(*dns.ResolverEnhancer))
	}

	resolver.DefaultResolver = r
	resolver.MainResolver = mr
	resolver.DefaultHostMapper = m
	if Tun.Enable && !strings.EqualFold(Tun.Stack, "gVisor") {
		resolver.DefaultLocalServer = dns.NewLocalServer(r, m)
	} else {
		resolver.DefaultLocalServer = nil
	}

	if c.Enable {
		dns.ReCreateServer(c.Listen, r, m)
	}
}

func updateHosts(tree *trie.DomainTrie) {
	resolver.DefaultHosts = tree
}

func updateProxies(proxies map[string]C.Proxy, providers map[string]provider.ProxyProvider) {
	tunnel.UpdateProxies(proxies, providers)
}

func updateRules(rules []C.Rule, ruleProviders map[string]*provider.RuleProvider) {
	tunnel.UpdateRules(rules, ruleProviders)
}

func loadProvider(ruleProviders map[string]*provider.RuleProvider, proxyProviders map[string]provider.ProxyProvider) {
	load := func(pv provider.Provider) {
		if pv.VehicleType() == provider.Compatible {
			log.Infoln("Start initial compatible provider %s", pv.Name())
		} else {
			log.Infoln("Start initial provider %s", (pv).Name())
		}

		if err := (pv).Initial(); err != nil {
			switch pv.Type() {
			case provider.Proxy:
				{
					log.Warnln("initial proxy provider %s error: %v", (pv).Name(), err)
				}
			case provider.Rule:
				{
					log.Warnln("initial rule provider %s error: %v", (pv).Name(), err)
				}

			}
		}
	}

	for _, proxyProvider := range proxyProviders {
		load(proxyProvider)
	}

	for _, ruleProvider := range ruleProviders {
		load(*ruleProvider)
	}
}

func updateGeneral(general *config.General, Tun *config.Tun, force bool) {
	tunnel.SetMode(general.Mode)
	resolver.DisableIPv6 = !general.IPv6
	adapter.UnifiedDelay.Store(general.UnifiedDelay)

	if (Tun.Enable || general.TProxyPort != 0) && general.Interface == "" {
		autoDetectInterfaceName, err := dev.GetAutoDetectInterface()
		if err == nil {
			if autoDetectInterfaceName != "" && autoDetectInterfaceName != "<nil>" {
				general.Interface = autoDetectInterfaceName
			} else {
				log.Debugln("Auto detect interface name is empty.")
			}
		} else {
			log.Debugln("Can not find auto detect interface. %s", err.Error())
		}
	}

	dialer.DefaultInterface.Store(general.Interface)

	log.Infoln("Use interface name: %s", general.Interface)

	iface.FlushCache()

	if !force {
		log.SetLevel(general.LogLevel)
		return
	}

	geodataLoader := general.GeodataLoader
	G.SetLoader(geodataLoader)

	allowLan := general.AllowLan
	P.SetAllowLan(allowLan)

	bindAddress := general.BindAddress
	P.SetBindAddress(bindAddress)

	tcpIn := tunnel.TCPIn()
	udpIn := tunnel.UDPIn()

	P.ReCreateHTTP(general.Port, tcpIn)
	P.ReCreateSocks(general.SocksPort, tcpIn, udpIn)
	P.ReCreateRedir(general.RedirPort, tcpIn, udpIn)
	P.ReCreateTProxy(general.TProxyPort, tcpIn, udpIn)
	P.ReCreateMixed(general.MixedPort, tcpIn, udpIn)

	log.SetLevel(general.LogLevel)
}

func updateTun(Tun *config.Tun) {
	if Tun == nil {
		return
	}

	tcpIn := tunnel.TCPIn()
	udpIn := tunnel.UDPIn()

	if err := P.ReCreateTun(*Tun, tcpIn, udpIn); err != nil {
		log.Errorln("Start Tun interface error: %s", err.Error())
		os.Exit(2)
	}
}

func updateUsers(users []auth.AuthUser) {
	authenticator := auth.NewAuthenticator(users)
	authStore.SetAuthenticator(authenticator)
	if authenticator != nil {
		log.Infoln("Authentication of local server updated")
	}
}

func updateProfile(cfg *config.Config) {
	profileCfg := cfg.Profile

	profile.StoreSelected.Store(profileCfg.StoreSelected)
	if profileCfg.StoreSelected {
		patchSelectGroup(cfg.Proxies)
	}
}

func patchSelectGroup(proxies map[string]C.Proxy) {
	mapping := cachefile.Cache().SelectedMap()
	if mapping == nil {
		return
	}

	for name, proxy := range proxies {
		outbound, ok := proxy.(*adapter.Proxy)
		if !ok {
			continue
		}

		selector, ok := outbound.ProxyAdapter.(*outboundgroup.Selector)
		if !ok {
			continue
		}

		selected, exist := mapping[name]
		if !exist {
			continue
		}

		selector.Set(selected)
	}
}

func updateIPTables(dns *config.DNS, general *config.General, tun *config.Tun) {
	if runtime.GOOS != "linux" || dns.Listen == "" || general.TProxyPort == 0 || tun.Enable || !general.AutoIptables {
		return
	}

	_, dnsPortStr, err := net.SplitHostPort(dns.Listen)
	if dnsPortStr == "0" || dnsPortStr == "" || err != nil {
		return
	}

	dnsPort, err := strconv.Atoi(dnsPortStr)
	if err != nil {
		return
	}

	tproxy.CleanUpTProxyLinuxIPTables()

	err = tproxy.SetTProxyLinuxIPTables(general.Interface, general.TProxyPort, dnsPort)

	if err != nil {
		log.Errorln("Can not setting iptables for TProxy on linux, %s", err.Error())
		os.Exit(2)
	}
}

func CleanUp() {
	P.CleanUp()
	if runtime.GOOS == "linux" {
		tproxy.CleanUpTProxyLinuxIPTables()
	}
}
