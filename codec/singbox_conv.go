package codec

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var allowedClashFieldsByType = map[string]string{
	"direct":    "",
	"http":      "username tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts",
	"socks5":    "username tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts",
	"ss":        "cipher udp-over-tcp udp-over-tcp-version plugin plugin-opts client-fingerprint smux",
	"vmess":     "uuid alterId cipher global-padding authenticated-length packet-encoding packet_encoding network ws-opts ws-headers grpc-opts h2-opts http-opts tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts smux",
	"vless":     "uuid flow encryption packet-encoding packet_encoding network ws-opts ws-headers grpc-opts h2-opts http-opts tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts smux",
	"trojan":    "network ws-opts ws-headers grpc-opts h2-opts http-opts tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts smux",
	"hysteria":  "ports hop-interval up down auth-str auth_str ca-str ca_str protocol obfs recv-window recv_window recv-window-conn recv_window_conn disable_mtu_discovery tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts",
	"hysteria2": "ports hop-interval up down obfs obfs-password tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts",
	"tuic":      "uuid ip congestion-controller udp-relay-mode udp-over-stream reduce-rtt heartbeat-interval tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts",
	"anytls":    "idle-session-check-interval idle-session-timeout min-idle-session tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts ech-opts",
	"wireguard": "ip ipv6 public-key pre-shared-key private-key reserved peers mtu",
	"snell":     "psk version reuse obfs-opts",
	"ssh":       "username private-key private-key-passphrase host-key host-key-algorithms client-version",
}

func convertClashNodeToSingBox(tag string, rawConfig map[string]any) (outbounds []map[string]any, endpoint map[string]any, err error) {
	c := CloneMap(rawConfig)
	delete(c, "__type")
	if err := normalizePacketEncoding(c); err != nil {
		return nil, nil, err
	}
	kind, _ := c["type"].(string)
	extraAllowed, supported := allowedClashFieldsByType[kind]
	if !supported {
		return nil, nil, fmt.Errorf("Clash protocol %q has no sing-box mapping", kind)
	}
	for _, legacy := range []string{"fingerprint", "fast-open", "udp-over-stream-version"} {
		if !emptyScalar(c[legacy]) {
			return nil, nil, fmt.Errorf("Clash field %q has no lossless sing-box mapping", legacy)
		}
	}
	allowed := " name type server port password dialer-proxy udp tfo mptcp hidden " + extraAllowed + " "
	for field, value := range c {
		if !strings.Contains(allowed, " "+field+" ") && !emptyScalar(value) {
			return nil, nil, fmt.Errorf("Clash %s field %q cannot be represented in sing-box", kind, field)
		}
	}
	if kind == "direct" {
		return []map[string]any{{"type": "direct", "tag": tag}}, nil, nil
	}
	if kind == "wireguard" {
		ep, err := convertClashWireGuardToSingBox(tag, c)
		return nil, ep, err
	}

	out := map[string]any{"tag": tag}
	server, _ := strOpt(c, "server")
	if server != "" {
		out["server"] = server
	}
	port, hasPort, err := portOpt(c, "port")
	if err != nil {
		return nil, nil, err
	}
	portsSpec, _ := strOpt(c, "ports")
	if hasPort {
		out["server_port"] = port
	} else if (kind != "hysteria" && kind != "hysteria2") || portsSpec == "" {
		return nil, nil, fmt.Errorf("Clash %s requires a valid port", kind)
	}

	tfo, err := boolOpt(c, "tfo")
	if err != nil {
		return nil, nil, err
	}
	mptcp, err := boolOpt(c, "mptcp")
	if err != nil {
		return nil, nil, err
	}
	if kind == "anytls" && tfo {
		return nil, nil, errors.New("AnyTLS tfo cannot be mapped")
	}

	mux, err := convertClashSmuxToSingBox(c["smux"])
	if err != nil {
		return nil, nil, err
	}
	if mux != nil {
		out["multiplex"] = mux
	}

	switch kind {
	case "http", "socks5":
		out["type"] = map[string]string{"http": "http", "socks5": "socks"}[kind]
		if user, _ := strOpt(c, "username"); user != "" {
			out["username"] = user
		}
		if pass, _ := strOpt(c, "password"); pass != "" {
			out["password"] = pass
		}
		tlsObj, err := convertClashTLSToSingBox(c, false)
		if err != nil {
			return nil, nil, err
		}
		if tlsObj != nil {
			out["tls"] = tlsObj
		}

	case "ss":
		out["type"] = "shadowsocks"
		if cipher, _ := strOpt(c, "cipher"); cipher != "" {
			out["method"] = cipher
		}
		if pass, _ := strOpt(c, "password"); pass != "" {
			out["password"] = pass
		}
		uot, err := boolOpt(c, "udp-over-tcp")
		if err != nil {
			return nil, nil, err
		}
		if uot {
			ver, _, err := intOpt(c, "udp-over-tcp-version")
			if err != nil {
				return nil, nil, err
			}
			if ver == 0 {
				ver = 1
			}
			out["udp_over_tcp"] = map[string]any{"enabled": true, "version": ver}
		}
		plugin, _ := strOpt(c, "plugin")
		if plugin == "shadow-tls" {
			pair, err := convertClashShadowTLSToSingBox(tag, c, out, tfo, mptcp)
			if err != nil {
				return nil, nil, err
			}
			return pair, nil, nil
		}
		if plugin != "" {
			if err := applyClashSSPluginToSingBox(plugin, c["plugin-opts"], out); err != nil {
				return nil, nil, err
			}
		}

	case "vmess", "vless", "trojan":
		out["type"] = kind
		forceTLS := kind == "trojan"
		tlsObj, err := convertClashTLSToSingBox(c, forceTLS)
		if err != nil {
			return nil, nil, err
		}
		if tlsObj != nil {
			out["tls"] = tlsObj
		}
		switch kind {
		case "vmess":
			if uuid, _ := strOpt(c, "uuid"); uuid != "" {
				out["uuid"] = uuid
			}
			if cipher, _ := strOpt(c, "cipher"); cipher != "" {
				out["security"] = cipher
			}
			if aid, ok, err := intOpt(c, "alterId"); err != nil {
				return nil, nil, err
			} else if ok && aid != 0 {
				out["alter_id"] = aid
			}
			if gp, err := boolOpt(c, "global-padding"); err != nil {
				return nil, nil, err
			} else if gp {
				out["global_padding"] = true
			}
			if al, err := boolOpt(c, "authenticated-length"); err != nil {
				return nil, nil, err
			} else if al {
				out["authenticated_length"] = true
			}
			if pe, err := clashPacketEncoding(c); err != nil {
				return nil, nil, err
			} else if pe != "" {
				out["packet_encoding"] = pe
			}
		case "vless":
			if uuid, _ := strOpt(c, "uuid"); uuid != "" {
				out["uuid"] = uuid
			}
			if enc, _ := strOpt(c, "encryption"); enc != "" && enc != "none" {
				return nil, nil, fmt.Errorf("VLESS encryption %q cannot be represented in sing-box", enc)
			}
			if pe, err := clashPacketEncoding(c); err != nil {
				return nil, nil, err
			} else if pe != "" {
				out["packet_encoding"] = pe
			}
			if flow, _ := strOpt(c, "flow"); flow != "" {
				netName, _ := strOpt(c, "network")
				if netName == "ws" {
					return nil, nil, errors.New("VLESS flow cannot be combined with WS safely")
				}
				if flow != "xtls-rprx-vision" {
					return nil, nil, fmt.Errorf("unsupported VLESS flow %q", flow)
				}
				out["flow"] = flow
			}
		case "trojan":
			if pass, _ := strOpt(c, "password"); pass != "" {
				out["password"] = pass
			}
		}
		tr, err := convertClashTransportToSingBox(c)
		if err != nil {
			return nil, nil, err
		}
		if tr != nil {
			out["transport"] = tr
		}

	case "hysteria":
		out["type"] = "hysteria"
		tlsObj, err := convertClashTLSToSingBox(c, true)
		if err != nil {
			return nil, nil, err
		}
		if ca, _ := strOptAlias(c, "ca-str", "ca_str"); ca != "" {
			tlsObj["certificate"] = []string{ca}
		}
		out["tls"] = tlsObj
		if portsSpec != "" {
			ranges, err := parseHopPortsToSingBox(portsSpec)
			if err != nil {
				return nil, nil, err
			}
			delete(out, "server_port")
			out["server_ports"] = ranges
		}
		if proto, _ := strOpt(c, "protocol"); proto != "" && proto != "udp" {
			return nil, nil, fmt.Errorf("Hysteria protocol %q is not supported in sing-box", proto)
		}
		if auth, _ := strOptAlias(c, "auth-str", "auth_str"); auth != "" {
			out["auth_str"] = auth
		}
		if obfsStr, _ := strOpt(c, "obfs"); obfsStr != "" {
			out["obfs"] = obfsStr
		}
		if upStr, _ := strOpt(c, "up"); upStr != "" {
			if mb, err := strconv.Atoi(upStr); err == nil {
				out["up_mbps"] = mb
			} else {
				out["up"] = upStr
			}
		}
		if downStr, _ := strOpt(c, "down"); downStr != "" {
			if mb, err := strconv.Atoi(downStr); err == nil {
				out["down_mbps"] = mb
			} else {
				out["down"] = downStr
			}
		}
		if rw, ok, err := intOptAlias(c, "recv-window", "recv_window"); err != nil {
			return nil, nil, err
		} else if ok && rw != 0 {
			out["recv_window"] = rw
		}
		if rwc, ok, err := intOptAlias(c, "recv-window-conn", "recv_window_conn"); err != nil {
			return nil, nil, err
		} else if ok && rwc != 0 {
			out["recv_window_conn"] = rwc
		}
		if dmd, err := boolOpt(c, "disable_mtu_discovery"); err != nil {
			return nil, nil, err
		} else if dmd {
			out["disable_mtu_discovery"] = true
		}
		if hop, ok, err := intOpt(c, "hop-interval"); err != nil {
			return nil, nil, err
		} else if ok && hop != 0 {
			out["hop_interval"] = fmt.Sprintf("%ds", hop)
		}

	case "hysteria2":
		out["type"] = "hysteria2"
		tlsObj, err := convertClashTLSToSingBox(c, true)
		if err != nil {
			return nil, nil, err
		}
		out["tls"] = tlsObj
		if portsSpec != "" {
			ranges, err := parseHopPortsToSingBox(portsSpec)
			if err != nil {
				return nil, nil, err
			}
			delete(out, "server_port")
			out["server_ports"] = ranges
		}
		if pass, _ := strOpt(c, "password"); pass != "" {
			out["password"] = pass
		}
		if upStr, _ := strOpt(c, "up"); upStr != "" {
			mb, err := rateToMbps(upStr)
			if err != nil {
				return nil, nil, err
			}
			if mb > 0 {
				out["up_mbps"] = mb
			}
		}
		if downStr, _ := strOpt(c, "down"); downStr != "" {
			mb, err := rateToMbps(downStr)
			if err != nil {
				return nil, nil, err
			}
			if mb > 0 {
				out["down_mbps"] = mb
			}
		}
		if hop, ok, err := intOpt(c, "hop-interval"); err != nil {
			return nil, nil, err
		} else if ok && hop != 0 {
			out["hop_interval"] = fmt.Sprintf("%ds", hop)
		}
		obfsType, _ := strOpt(c, "obfs")
		obfsPass, _ := strOpt(c, "obfs-password")
		if (obfsType != "") != (obfsPass != "") {
			return nil, nil, errors.New("Hysteria2 obfs requires both obfs and obfs-password")
		}
		if obfsType != "" {
			out["obfs"] = map[string]any{"type": obfsType, "password": obfsPass}
		}

	case "tuic":
		out["type"] = "tuic"
		tlsObj, err := convertClashTLSToSingBox(c, true)
		if err != nil {
			return nil, nil, err
		}
		out["tls"] = tlsObj
		if ip, _ := strOpt(c, "ip"); ip != "" {
			out["server"] = ip
		}
		if uuid, _ := strOpt(c, "uuid"); uuid != "" {
			out["uuid"] = uuid
		}
		if pass, _ := strOpt(c, "password"); pass != "" {
			out["password"] = pass
		}
		if cc, _ := strOpt(c, "congestion-controller"); cc != "" {
			out["congestion_control"] = cc
		}
		if mode, _ := strOpt(c, "udp-relay-mode"); mode != "" {
			out["udp_relay_mode"] = mode
		}
		if uos, err := boolOpt(c, "udp-over-stream"); err != nil {
			return nil, nil, err
		} else if uos {
			out["udp_over_stream"] = true
		}
		if rtt, err := boolOpt(c, "reduce-rtt"); err != nil {
			return nil, nil, err
		} else if rtt {
			out["zero_rtt_handshake"] = true
		}
		if hb, ok, err := intOpt(c, "heartbeat-interval"); err != nil {
			return nil, nil, err
		} else if ok && hb != 0 {
			out["heartbeat"] = fmt.Sprintf("%dms", hb)
		}

	case "anytls":
		out["type"] = "anytls"
		tlsObj, err := convertClashTLSToSingBox(c, true)
		if err != nil {
			return nil, nil, err
		}
		out["tls"] = tlsObj
		if pass, _ := strOpt(c, "password"); pass != "" {
			out["password"] = pass
		}
		if checkInt, ok, err := intOpt(c, "idle-session-check-interval"); err != nil {
			return nil, nil, err
		} else if ok && checkInt != 0 {
			out["idle_session_check_interval"] = fmt.Sprintf("%ds", checkInt)
		}
		if timeout, ok, err := intOpt(c, "idle-session-timeout"); err != nil {
			return nil, nil, err
		} else if ok && timeout != 0 {
			out["idle_session_timeout"] = fmt.Sprintf("%ds", timeout)
		}
		if minIdle, ok, err := intOpt(c, "min-idle-session"); err != nil {
			return nil, nil, err
		} else if ok && minIdle != 0 {
			out["min_idle_session"] = minIdle
		}

	case "snell":
		out["type"] = "snell"
		ver, hasVer, err := intOpt(c, "version")
		if err != nil {
			return nil, nil, err
		}
		if !hasVer || ver == 0 {
			// Mihomo DefaultSnellVersion is 1 unless explicitly specified; sing-box supports Snell v4/v6.
			return nil, nil, errors.New("Snell requires version 4 for sing-box")
		}
		if ver == 5 {
			ver = 4
		}
		if ver != 4 {
			return nil, nil, fmt.Errorf("Snell version %d is not supported in sing-box (requires version 4)", ver)
		}
		out["version"] = 4
		psk, _ := strOpt(c, "psk")
		if psk == "" {
			return nil, nil, errors.New("Snell psk is required")
		}
		out["psk"] = psk
		if reuse, err := boolOpt(c, "reuse"); err != nil {
			return nil, nil, err
		} else if reuse {
			out["reuse"] = true
		}
		if rawObfs := c["obfs-opts"]; rawObfs != nil {
			obfsMap, ok := rawObfs.(map[string]any)
			if !ok {
				return nil, nil, errors.New("invalid Snell obfs-opts")
			}
			if err := checkMapKeys(obfsMap, "mode host", "snell.obfs-opts"); err != nil {
				return nil, nil, err
			}
			mode, _ := strOpt(obfsMap, "mode")
			if mode != "" && mode != "none" && mode != "http" && mode != "tls" {
				return nil, nil, fmt.Errorf("unsupported Snell obfs mode %q", mode)
			}
			if mode != "" {
				out["obfs_mode"] = mode
			}
			if host, _ := strOpt(obfsMap, "host"); host != "" {
				out["obfs_host"] = host
			}
		}

	case "ssh":
		out["type"] = "ssh"
		if user, _ := strOpt(c, "username"); user != "" {
			out["user"] = user
		}
		if pass, _ := strOpt(c, "password"); pass != "" {
			out["password"] = pass
		}
		if pk, _ := strOpt(c, "private-key"); pk != "" {
			if !strings.Contains(pk, "PRIVATE KEY") {
				return nil, nil, errors.New("SSH private-key file paths cannot be converted safely; use inline PEM")
			}
			out["private_key"] = pk
		}
		if pass, _ := strOpt(c, "private-key-passphrase"); pass != "" {
			out["private_key_passphrase"] = pass
		}
		if hk, err := strSliceOpt(c, "host-key"); err != nil {
			return nil, nil, err
		} else if len(hk) > 0 {
			out["host_key"] = hk
		}
		if hka, err := strSliceOpt(c, "host-key-algorithms"); err != nil {
			return nil, nil, err
		} else if len(hka) > 0 {
			out["host_key_algorithms"] = hka
		}
		if cv, _ := strOpt(c, "client-version"); cv != "" {
			out["client_version"] = cv
		}
	}

	if tfo {
		out["tcp_fast_open"] = true
	}
	if mptcp {
		out["tcp_multi_path"] = true
	}
	if c["udp"] == false {
		switch kind {
		case "ss", "socks5", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "snell":
			out["network"] = "tcp"
		}
	}
	return []map[string]any{out}, nil, nil
}

func convertClashTLSToSingBox(c map[string]any, forceEnabled bool) (map[string]any, error) {
	tlsEnabled, err := boolOpt(c, "tls")
	if err != nil {
		return nil, err
	}
	if forceEnabled {
		tlsEnabled = true
	}
	var realityOut map[string]any
	if rawReality := c["reality-opts"]; rawReality != nil {
		rm, ok := rawReality.(map[string]any)
		if !ok {
			return nil, errors.New("invalid reality-opts")
		}
		if err := checkMapKeys(rm, "public-key short-id", "reality-opts"); err != nil {
			return nil, err
		}
		pk, _ := strOpt(rm, "public-key")
		if pk != "" {
			tlsEnabled = true
			realityOut = map[string]any{"enabled": true, "public_key": pk}
			if sid, _ := strOpt(rm, "short-id"); sid != "" {
				realityOut["short_id"] = sid
			}
		}
	}
	var echOut map[string]any
	if rawECH := c["ech-opts"]; rawECH != nil {
		em, ok := rawECH.(map[string]any)
		if !ok {
			return nil, errors.New("invalid ech-opts")
		}
		if err := checkMapKeys(em, "enable config query-server-name", "ech-opts"); err != nil {
			return nil, err
		}
		en, err := boolOpt(em, "enable")
		if err != nil {
			return nil, err
		}
		if en {
			tlsEnabled = true
			echOut = map[string]any{"enabled": true}
			if cfg, _ := strOpt(em, "config"); cfg != "" {
				echOut["config"] = []string{cfg}
			}
			if qsn, _ := strOpt(em, "query-server-name"); qsn != "" {
				echOut["query_server_name"] = qsn
			}
		}
	}
	if !tlsEnabled {
		return nil, nil
	}
	tlsObj := map[string]any{"enabled": true}
	if disableSNI, err := boolOpt(c, "disable-sni"); err != nil {
		return nil, err
	} else if disableSNI {
		tlsObj["disable_sni"] = true
	}
	serverName, _ := strOpt(c, "servername")
	if serverName == "" {
		serverName, _ = strOpt(c, "sni")
	}
	if serverName == "" {
		serverName, _ = strOpt(c, "server")
	}
	if serverName != "" {
		tlsObj["server_name"] = serverName
	}
	if insecure, err := boolOpt(c, "skip-cert-verify"); err != nil {
		return nil, err
	} else if insecure {
		tlsObj["insecure"] = true
	}
	if alpn, err := strSliceOpt(c, "alpn"); err != nil {
		return nil, err
	} else if len(alpn) > 0 {
		tlsObj["alpn"] = alpn
	}
	if fp, _ := strOpt(c, "client-fingerprint"); fp != "" {
		tlsObj["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if realityOut != nil {
		tlsObj["reality"] = realityOut
	}
	if echOut != nil {
		tlsObj["ech"] = echOut
	}
	return tlsObj, nil
}

func convertClashSmuxToSingBox(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("invalid smux configuration")
	}
	if err := checkMapKeys(m, "enabled protocol max-connections min-streams max-streams padding brutal-opts", "smux"); err != nil {
		return nil, err
	}
	enabled, err := boolOpt(m, "enabled")
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	out := map[string]any{"enabled": true}
	if proto, _ := strOpt(m, "protocol"); proto != "" {
		out["protocol"] = proto
	}
	if pad, err := boolOpt(m, "padding"); err != nil {
		return nil, err
	} else if pad {
		out["padding"] = true
	}
	maxStreams, _, err := intOpt(m, "max-streams")
	if err != nil {
		return nil, err
	}
	minStreams, _, err := intOpt(m, "min-streams")
	if err != nil {
		return nil, err
	}
	maxConn, _, err := intOpt(m, "max-connections")
	if err != nil {
		return nil, err
	}
	if maxStreams > 0 {
		out["max_streams"] = maxStreams
	} else {
		out["min_streams"] = max(minStreams, 4)
		out["max_connections"] = max(maxConn, 4)
	}
	if rawBrutal := m["brutal-opts"]; rawBrutal != nil {
		bm, ok := rawBrutal.(map[string]any)
		if !ok {
			return nil, errors.New("invalid smux.brutal-opts")
		}
		if err := checkMapKeys(bm, "enabled up down", "smux.brutal-opts"); err != nil {
			return nil, err
		}
		bEnabled, err := boolOpt(bm, "enabled")
		if err != nil {
			return nil, err
		}
		if bEnabled {
			brutal := map[string]any{"enabled": true}
			if upStr, _ := strOpt(bm, "up"); upStr != "" {
				upMbps, err := rateToMbps(upStr)
				if err != nil {
					return nil, err
				}
				brutal["up_mbps"] = upMbps
			}
			if downStr, _ := strOpt(bm, "down"); downStr != "" {
				downMbps, err := rateToMbps(downStr)
				if err != nil {
					return nil, err
				}
				brutal["down_mbps"] = downMbps
			}
			out["brutal"] = brutal
		}
	}
	return out, nil
}

func convertClashTransportToSingBox(c map[string]any) (map[string]any, error) {
	network, _ := strOpt(c, "network")
	if network != "" && network != "tcp" && network != "ws" && network != "grpc" && network != "h2" && network != "http" && network != "httpupgrade" {
		return nil, fmt.Errorf("unsupported transport %q", network)
	}

	var wsOpts map[string]any
	if raw := c["ws-opts"]; raw != nil {
		var ok bool
		wsOpts, ok = raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid ws-opts")
		}
		if err := checkMapKeys(wsOpts, "path headers max-early-data early-data-header-name v2ray-http-upgrade v2ray-http-upgrade-fast-open", "ws-opts"); err != nil {
			return nil, err
		}
	}
	var grpcOpts map[string]any
	if raw := c["grpc-opts"]; raw != nil {
		var ok bool
		grpcOpts, ok = raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid grpc-opts")
		}
		if err := checkMapKeys(grpcOpts, "grpc-service-name", "grpc-opts"); err != nil {
			return nil, err
		}
	}
	var h2Opts map[string]any
	if raw := c["h2-opts"]; raw != nil {
		var ok bool
		h2Opts, ok = raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid h2-opts")
		}
		if err := checkMapKeys(h2Opts, "host path", "h2-opts"); err != nil {
			return nil, err
		}
	}
	var httpOpts map[string]any
	if raw := c["http-opts"]; raw != nil {
		var ok bool
		httpOpts, ok = raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid http-opts")
		}
		if err := checkMapKeys(httpOpts, "headers method path", "http-opts"); err != nil {
			return nil, err
		}
	}

	wsPath, _ := strOpt(wsOpts, "path")
	if network == "ws" || network == "httpupgrade" || wsPath != "" || wsOpts["v2ray-http-upgrade"] == true {
		isUpgrade := network == "httpupgrade" || wsOpts["v2ray-http-upgrade"] == true
		if fastOpen, _ := boolOpt(wsOpts, "v2ray-http-upgrade-fast-open"); fastOpen {
			return nil, errors.New("HTTPUpgrade fast-open cannot be mapped to sing-box")
		}
		headers, err := headerMapOpt(c["ws-headers"], false)
		if err != nil {
			return nil, err
		}
		if len(headers) == 0 && wsOpts != nil {
			headers, err = headerMapOpt(wsOpts["headers"], false)
			if err != nil {
				return nil, err
			}
		}
		ed, _, err := intOpt(wsOpts, "max-early-data")
		if err != nil {
			return nil, err
		}
		eh, _ := strOpt(wsOpts, "early-data-header-name")
		if isUpgrade {
			if ed > 0 || eh != "" {
				return nil, errors.New("HTTPUpgrade early-data options cannot be mapped to sing-box")
			}
			tr := map[string]any{"type": "httpupgrade"}
			if wsPath != "" {
				tr["path"] = wsPath
			}
			host, _ := strOpt(c, "servername")
			if host == "" && len(headers["Host"]) > 0 {
				host = headers["Host"][0]
			}
			if host != "" {
				tr["host"] = host
			}
			if len(headers) > 0 {
				tr["headers"] = headers
			}
			return tr, nil
		}
		tr := map[string]any{"type": "ws"}
		if wsPath != "" {
			tr["path"] = wsPath
		}
		if len(headers) > 0 {
			tr["headers"] = headers
		}
		if ed > 0 {
			tr["max_early_data"] = ed
		}
		if eh != "" {
			tr["early_data_header_name"] = eh
		}
		return tr, nil
	}

	grpcSvc, _ := strOpt(grpcOpts, "grpc-service-name")
	if network == "grpc" || grpcSvc != "" {
		tr := map[string]any{"type": "grpc"}
		if grpcSvc != "" {
			tr["service_name"] = grpcSvc
		}
		return tr, nil
	}

	h2Path, _ := strOpt(h2Opts, "path")
	if network == "h2" || h2Path != "" {
		tr := map[string]any{"type": "http"}
		if h2Path != "" {
			tr["path"] = h2Path
		}
		if hosts, err := strSliceOpt(h2Opts, "host"); err != nil {
			return nil, err
		} else if len(hosts) > 0 {
			tr["host"] = hosts
		}
		return tr, nil
	}

	if network == "http" || httpOpts != nil {
		tr := map[string]any{"type": "http"}
		if paths, err := strSliceOpt(httpOpts, "path"); err != nil {
			return nil, err
		} else if len(paths) > 1 {
			return nil, errors.New("multiple HTTP paths cannot be mapped")
		} else if len(paths) == 1 && paths[0] != "" {
			tr["path"] = paths[0]
		}
		if method, _ := strOpt(httpOpts, "method"); method != "" {
			tr["method"] = method
		}
		if headers, err := headerMapOpt(httpOpts["headers"], true); err != nil {
			return nil, err
		} else if len(headers) > 0 {
			if len(headers["Host"]) > 0 {
				tr["host"] = headers["Host"]
			}
			tr["headers"] = headers
		}
		return tr, nil
	}

	return nil, nil
}

func convertClashShadowTLSToSingBox(tag string, c, ssOut map[string]any, tfo, mptcp bool) ([]map[string]any, error) {
	var opts map[string]any
	if raw := c["plugin-opts"]; raw != nil {
		var ok bool
		opts, ok = raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid shadow-tls plugin-opts")
		}
		if err := checkMapKeys(opts, "host password version alpn skip-cert-verify", "plugin-opts"); err != nil {
			return nil, err
		}
	}
	helperTag := tag + "-shadowtls"
	server := ssOut["server"]
	serverPort := ssOut["server_port"]
	delete(ssOut, "server")
	delete(ssOut, "server_port")
	ssOut["detour"] = helperTag
	if c["udp"] == false {
		ssOut["network"] = "tcp"
	}

	tlsMap := map[string]any{"enabled": true}
	if host, _ := strOpt(opts, "host"); host != "" {
		tlsMap["server_name"] = host
	}
	if alpn, err := strSliceOpt(opts, "alpn"); err != nil {
		return nil, err
	} else if len(alpn) > 0 {
		tlsMap["alpn"] = alpn
	}
	if insecure, err := boolOpt(opts, "skip-cert-verify"); err != nil {
		return nil, err
	} else if insecure {
		tlsMap["insecure"] = true
	}
	if fp, _ := strOpt(c, "client-fingerprint"); fp != "" {
		tlsMap["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}

	helper := map[string]any{
		"type":        "shadowtls",
		"tag":         helperTag,
		"server":      server,
		"server_port": serverPort,
		"tls":         tlsMap,
	}
	if ver, ok, err := intOpt(opts, "version"); err != nil {
		return nil, err
	} else if ok && ver != 0 {
		helper["version"] = ver
	}
	if pass, _ := strOpt(opts, "password"); pass != "" {
		helper["password"] = pass
	}
	if tfo {
		helper["tcp_fast_open"] = true
	}
	if mptcp {
		helper["tcp_multi_path"] = true
	}
	return []map[string]any{ssOut, helper}, nil
}

func applyClashSSPluginToSingBox(plugin string, rawOpts any, out map[string]any) error {
	var opts map[string]any
	if rawOpts != nil {
		var ok bool
		opts, ok = rawOpts.(map[string]any)
		if !ok {
			return errors.New("invalid plugin-opts")
		}
	}
	switch plugin {
	case "obfs":
		if err := checkMapKeys(opts, "mode host", "plugin-opts"); err != nil {
			return err
		}
		out["plugin"] = "obfs-local"
		var parts []string
		if mode, _ := strOpt(opts, "mode"); mode != "" {
			parts = append(parts, "obfs="+pluginBackslashEscape(mode))
		}
		if host, _ := strOpt(opts, "host"); host != "" {
			parts = append(parts, "obfs-host="+pluginBackslashEscape(host))
		}
		if len(parts) > 0 {
			out["plugin_opts"] = strings.Join(parts, ";")
		}
		return nil
	case "v2ray-plugin":
		if err := checkMapKeys(opts, "mode tls host path mux", "plugin-opts"); err != nil {
			return err
		}
		out["plugin"] = "v2ray-plugin"
		var parts []string
		if tlsOn, err := boolOpt(opts, "tls"); err != nil {
			return err
		} else if tlsOn {
			parts = append(parts, "tls")
		}
		if host, _ := strOpt(opts, "host"); host != "" {
			parts = append(parts, "host="+pluginBackslashEscape(host))
		}
		if path, _ := strOpt(opts, "path"); path != "" {
			parts = append(parts, "path="+pluginBackslashEscape(path))
		}
		if mode, _ := strOpt(opts, "mode"); mode != "" {
			parts = append(parts, "mode="+pluginBackslashEscape(mode))
		}
		if muxOn, err := boolOpt(opts, "mux"); err != nil {
			return err
		} else if muxOn {
			parts = append(parts, "mux")
		}
		if len(parts) > 0 {
			out["plugin_opts"] = strings.Join(parts, ";")
		}
		return nil
	default:
		return fmt.Errorf("unsupported SS plugin %q", plugin)
	}
}

func convertClashWireGuardToSingBox(tag string, c map[string]any) (map[string]any, error) {
	mtu, hasMTU, err := intOpt(c, "mtu")
	if err != nil {
		return nil, err
	}
	if hasMTU && (mtu < 0 || mtu > 65535) {
		return nil, errors.New("invalid WireGuard MTU")
	}
	ip4, _ := strOpt(c, "ip")
	ip6, _ := strOpt(c, "ipv6")
	addresses, err := wireguardPrefixes([]string{ip4, ip6})
	if err != nil {
		return nil, err
	}
	ep := map[string]any{
		"type":    "wireguard",
		"tag":     tag,
		"address": addresses,
	}
	if pk, _ := strOpt(c, "private-key"); pk != "" {
		ep["private_key"] = pk
	}
	if hasMTU && mtu > 0 {
		ep["mtu"] = mtu
	}

	if rawPeers, ok := c["peers"].([]any); ok && len(rawPeers) > 0 {
		peers := make([]any, 0, len(rawPeers))
		for _, raw := range rawPeers {
			pm, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid WireGuard peer")
			}
			if !emptyScalar(pm["ip"]) || !emptyScalar(pm["ipv6"]) {
				return nil, errors.New("WireGuard peer IP fields cannot be mapped; use allowed-ips")
			}
			if err := checkMapKeys(pm, "server port public-key pre-shared-key allowed-ips allowed_ips reserved", "wireguard.peers"); err != nil {
				return nil, err
			}
			peerPort, okPort, err := portOpt(pm, "port")
			if err != nil || !okPort {
				return nil, errors.New("WireGuard peer port out of range")
			}
			sp := map[string]any{"port": peerPort}
			if srv, _ := strOpt(pm, "server"); srv != "" {
				sp["address"] = srv
			}
			if pub, _ := strOpt(pm, "public-key"); pub != "" {
				sp["public_key"] = pub
			}
			if psk, _ := strOpt(pm, "pre-shared-key"); psk != "" {
				sp["pre_shared_key"] = psk
			}
			allowed, err := strSliceOpt(pm, "allowed-ips")
			if err != nil {
				return nil, err
			}
			if len(allowed) == 0 {
				allowed, err = strSliceOpt(pm, "allowed_ips")
				if err != nil {
					return nil, err
				}
			}
			if len(allowed) > 0 {
				sp["allowed_ips"] = allowed
			}
			if res, err := wireguardReservedOpt(pm["reserved"]); err != nil {
				return nil, err
			} else if res != nil {
				sp["reserved"] = res
			}
			peers = append(peers, sp)
		}
		ep["peers"] = peers
		return ep, nil
	}

	port, okPort, err := portOpt(c, "port")
	if err != nil || !okPort {
		return nil, errors.New("WireGuard server port out of range")
	}
	var allowed []string
	for _, addr := range addresses {
		prefix, err := netip.ParsePrefix(addr)
		if err != nil {
			return nil, err
		}
		if prefix.Addr().Is4() {
			allowed = append(allowed, "0.0.0.0/0")
		} else {
			allowed = append(allowed, "::/0")
		}
	}
	peer := map[string]any{"port": port}
	if srv, _ := strOpt(c, "server"); srv != "" {
		peer["address"] = srv
	}
	if pub, _ := strOpt(c, "public-key"); pub != "" {
		peer["public_key"] = pub
	}
	if psk, _ := strOpt(c, "pre-shared-key"); psk != "" {
		peer["pre_shared_key"] = psk
	}
	if len(allowed) > 0 {
		peer["allowed_ips"] = allowed
	}
	if res, err := wireguardReservedOpt(c["reserved"]); err != nil {
		return nil, err
	} else if res != nil {
		peer["reserved"] = res
	}
	ep["peers"] = []any{peer}
	return ep, nil
}

func clashPacketEncoding(c map[string]any) (string, error) {
	for _, field := range []string{"packet-encoding", "packet_encoding"} {
		if value, ok := c[field].(string); ok && value != "" {
			if value != "packetaddr" && value != "packet" && value != "xudp" {
				return "", fmt.Errorf("unmapped packet encoding %q", value)
			}
			return value, nil
		}
	}
	return "", nil
}

func checkMapKeys(m map[string]any, allowed, scope string) error {
	for k, v := range m {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") && !emptyScalar(v) {
			return fmt.Errorf("Clash field \"%s.%s\" cannot be represented in sing-box", scope, k)
		}
	}
	return nil
}

func strOpt(m map[string]any, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	raw, exists := m[key]
	if !exists || raw == nil {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		return v, true
	case int, int64, int32, uint, uint64, uint32, float64:
		return fmt.Sprint(v), true
	default:
		return "", false
	}
}

func strOptAlias(m map[string]any, primary, secondary string) (string, bool) {
	if s, ok := strOpt(m, primary); ok && s != "" {
		return s, true
	}
	return strOpt(m, secondary)
}

func intOpt(m map[string]any, key string) (int, bool, error) {
	if m == nil {
		return 0, false, nil
	}
	raw, exists := m[key]
	if !exists || raw == nil || raw == "" {
		return 0, false, nil
	}
	v, ok := parseIntValue(raw)
	if !ok {
		return 0, true, fmt.Errorf("invalid integer field %q", key)
	}
	return v, true, nil
}

func intOptAlias(m map[string]any, primary, secondary string) (int, bool, error) {
	v, ok, err := intOpt(m, primary)
	if err != nil || (ok && v != 0) {
		return v, ok, err
	}
	return intOpt(m, secondary)
}

func portOpt(m map[string]any, key string) (int, bool, error) {
	v, ok, err := intOpt(m, key)
	if err != nil || !ok {
		return 0, ok, err
	}
	if v < 1 || v > 65535 {
		return 0, true, fmt.Errorf("port %d out of range", v)
	}
	return v, true, nil
}

func boolOpt(m map[string]any, key string) (bool, error) {
	if m == nil {
		return false, nil
	}
	raw, exists := m[key]
	if !exists || raw == nil {
		return false, nil
	}
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		if v == "" {
			return false, nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("invalid boolean field %q", key)
		}
		return b, nil
	default:
		return false, fmt.Errorf("invalid boolean field %q", key)
	}
}

func strSliceOpt(m map[string]any, key string) ([]string, error) {
	if m == nil {
		return nil, nil
	}
	raw, exists := m[key]
	if !exists || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("invalid string list element in %q", key)
			}
			out = append(out, s)
		}
		return out, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	default:
		return nil, fmt.Errorf("invalid string list field %q", key)
	}
}

func headerMapOpt(raw any, multi bool) (map[string][]string, error) {
	if raw == nil {
		return nil, nil
	}
	out := map[string][]string{}
	switch m := raw.(type) {
	case map[string]string:
		for k, v := range m {
			out[k] = []string{v}
		}
	case map[string][]string:
		for k, v := range m {
			cp := make([]string, len(v))
			copy(cp, v)
			out[k] = cp
		}
	case map[string]any:
		for k, v := range m {
			switch item := v.(type) {
			case string:
				out[k] = []string{item}
			case []string:
				cp := make([]string, len(item))
				copy(cp, item)
				out[k] = cp
			case []any:
				if !multi {
					return nil, fmt.Errorf("header %q expects single string value", k)
				}
				vals := make([]string, 0, len(item))
				for _, elem := range item {
					s, ok := elem.(string)
					if !ok {
						return nil, fmt.Errorf("invalid header value in %q", k)
					}
					vals = append(vals, s)
				}
				out[k] = vals
			default:
				return nil, fmt.Errorf("invalid header value in %q", k)
			}
		}
	default:
		return nil, errors.New("invalid headers map")
	}
	return out, nil
}

func wireguardPrefixes(ips []string) ([]string, error) {
	out := make([]string, 0, len(ips))
	for _, raw := range ips {
		if raw == "" {
			continue
		}
		if p, err := netip.ParsePrefix(raw); err == nil {
			out = append(out, p.String())
			continue
		}
		ip, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid WireGuard IP %q: %w", raw, err)
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()).String())
	}
	return out, nil
}

func wireguardReservedOpt(raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		return v, nil
	case []int:
		if len(v) == 0 {
			return nil, nil
		}
		if len(v) != 3 {
			return nil, errors.New("WireGuard reserved array must have 3 bytes")
		}
		for _, n := range v {
			if n < 0 || n > 255 {
				return nil, errors.New("WireGuard reserved byte out of range")
			}
		}
		cp := make([]int, 3)
		copy(cp, v)
		return cp, nil
	case []any:
		if len(v) == 0 {
			return nil, nil
		}
		if len(v) != 3 {
			return nil, errors.New("WireGuard reserved array must have 3 bytes")
		}
		out := make([]int, 3)
		for i, elem := range v {
			n, ok := parseIntValue(elem)
			if !ok || n < 0 || n > 255 {
				return nil, errors.New("WireGuard reserved byte out of range")
			}
			out[i] = n
		}
		return out, nil
	default:
		return nil, errors.New("invalid WireGuard reserved value")
	}
}

func parseHopPortsToSingBox(ports string) ([]string, error) {
	var tokens []string
	for _, part := range strings.Split(ports, ",") {
		for _, sub := range strings.Split(part, "/") {
			sub = strings.TrimSpace(sub)
			if sub != "" {
				tokens = append(tokens, sub)
			}
		}
	}
	if len(tokens) == 0 {
		return nil, errors.New("empty hop ports")
	}
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if startStr, endStr, ok := strings.Cut(tok, "-"); ok {
			start, err1 := strconv.Atoi(startStr)
			end, err2 := strconv.Atoi(endStr)
			if err1 != nil || err2 != nil || start < 1 || end > 65535 || end < start {
				return nil, fmt.Errorf("invalid hop port range %q", tok)
			}
			out = append(out, fmt.Sprintf("%d:%d", start, end))
			continue
		}
		p, err := strconv.Atoi(tok)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid hop port %q", tok)
		}
		out = append(out, fmt.Sprintf("%d:%d", p, p))
	}
	return out, nil
}

var rateStringPattern = regexp.MustCompile(`^(\d+)\s*([KMGT]?)([Bb])ps$`)

func rateToMbps(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if mb, err := strconv.Atoi(s); err == nil {
		return mb, nil
	}
	m := rateStringPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("unsupported bandwidth rate %q", s)
	}
	scale := 1.0
	switch m[2] {
	case "K":
		scale = 1.0 / 1000.0
	case "M":
		scale = 1.0
	case "G":
		scale = 1000.0
	case "T":
		scale = 1000.0 * 1000.0
	}
	if m[3] == "B" {
		scale *= 8.0
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, err
	}
	mb := int(float64(v) * scale)
	if mb == 0 && v > 0 {
		mb = 1
	}
	return mb, nil
}

func pluginBackslashEscape(s string) string {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == '\\' || b == '=' || b == ';' {
			buf.WriteByte('\\')
		}
		buf.WriteByte(b)
	}
	return buf.String()
}
