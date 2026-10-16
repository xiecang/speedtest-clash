// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func setURIQuery(q url.Values, c map[string]any, field, param string) {
	if value := c[field]; value != nil && !emptyScalar(value) {
		q.Set(param, fmt.Sprint(value))
	}
}

func uriStringList(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var values []string
	if err = json.Unmarshal(raw, &values); err != nil {
		return "", errors.New("URI requires a string array")
	}
	for _, value := range values {
		if value == "" || strings.Contains(value, ",") {
			return "", errors.New("URI array values must be nonempty and comma-free")
		}
	}
	return strings.Join(values, ","), nil
}

func uriFields(c map[string]any, allowed string) error {
	for field, value := range c {
		if !emptyScalar(value) && !strings.Contains(" "+allowed+" ", " "+field+" ") {
			return fmt.Errorf("URI cannot preserve field %q", field)
		}
	}
	return nil
}

func encodeURITransport(c map[string]any) (url.Values, error) {
	q := url.Values{}
	network, _ := c["network"].(string)
	if network == "" || network == "tcp" {
		return q, nil
	}
	q.Set("type", network)
	var opts map[string]any
	switch network {
	case "ws", "httpupgrade":
		opts, _ = c["ws-opts"].(map[string]any)
		if err := uriFields(opts, "path headers v2ray-http-upgrade max-early-data early-data-header-name v2ray-http-upgrade-fast-open"); err != nil {
			return nil, err
		}
		setURIQuery(q, opts, "path", "path")
		if headers, ok := opts["headers"].(map[string]any); ok {
			if err := uriFields(headers, "Host"); err != nil {
				return nil, err
			}
			setURIQuery(q, headers, "Host", "host")
		}
		if opts["v2ray-http-upgrade"] == true {
			q.Set("type", "httpupgrade")
		}
		setURIQuery(q, opts, "max-early-data", "ed")
		setURIQuery(q, opts, "early-data-header-name", "eh")
		if opts["v2ray-http-upgrade-fast-open"] == true {
			q.Set("ed", "1")
		}
	case "grpc":
		opts, _ = c["grpc-opts"].(map[string]any)
		if err := uriFields(opts, "grpc-service-name grpc-user-agent"); err != nil {
			return nil, err
		}
		setURIQuery(q, opts, "grpc-service-name", "serviceName")
		setURIQuery(q, opts, "grpc-user-agent", "grpcUserAgent")
	case "http":
		q.Set("type", "tcp")
		q.Set("headerType", "http")
		opts, _ = c["http-opts"].(map[string]any)
		if err := uriFields(opts, "path headers method"); err != nil {
			return nil, err
		}
		if opts["path"] != nil {
			text, err := uriStringList(opts["path"])
			if err != nil {
				return nil, err
			}
			if strings.Contains(text, ",") {
				return nil, errors.New("URI cannot preserve multiple HTTP paths")
			}
			q.Set("path", text)
		}
		if headers, ok := opts["headers"].(map[string]any); ok {
			if err := uriFields(headers, "Host"); err != nil {
				return nil, err
			}
			if headers["Host"] != nil {
				text, err := uriStringList(headers["Host"])
				if err != nil {
					return nil, err
				}
				q.Set("host", text)
			}
		}
		setURIQuery(q, opts, "method", "method")
	case "h2":
		opts, _ = c["h2-opts"].(map[string]any)
		if err := uriFields(opts, "path host"); err != nil {
			return nil, err
		}
		setURIQuery(q, opts, "path", "path")
		if opts["host"] != nil {
			text, err := uriStringList(opts["host"])
			if err != nil {
				return nil, err
			}
			q.Set("host", text)
		}
	case "xhttp":
		opts, _ = c["xhttp-opts"].(map[string]any)
		for _, field := range []string{"path", "host", "mode"} {
			setURIQuery(q, opts, field, field)
		}
		extra, err := encodeXHTTPExtra(opts, false)
		if err != nil {
			return nil, err
		}
		if len(extra) > 0 {
			data, err := json.Marshal(extra)
			if err != nil {
				return nil, err
			}
			q.Set("extra", string(data))
		}
	default:
		return nil, fmt.Errorf("URI unsupported transport %s", network)
	}
	return q, nil
}

var xhttpFields = map[string]string{
	"no-grpc-header": "noGRPCHeaders", "x-padding-bytes": "xPaddingBytes", "x-padding-obfs-mode": "xPaddingObfsMode", "x-padding-key": "xPaddingKey", "x-padding-header": "xPaddingHeader", "x-padding-placement": "xPaddingPlacement", "x-padding-method": "xPaddingMethod", "uplink-http-method": "uplinkHttpMethod", "session-placement": "sessionPlacement", "session-key": "sessionKey", "seq-placement": "seqPlacement", "seq-key": "seqKey", "uplink-data-placement": "uplinkDataPlacement", "uplink-data-key": "uplinkDataKey", "uplink-chunk-size": "uplinkChunkSize", "sc-max-each-post-bytes": "scMaxEachPostBytes", "sc-min-posts-interval-ms": "scMinPostsIntervalMs",
}
var reuseFields = map[string]string{"max-connections": "maxConnections", "max-concurrency": "maxConcurrency", "c-max-reuse-times": "cMaxReuseTimes", "h-max-request-times": "hMaxRequestTimes", "h-max-reusable-secs": "hMaxReusableSecs", "h-keep-alive-period": "hKeepAlivePeriod"}

func encodeXHTTPExtra(opts map[string]any, download bool) (map[string]any, error) {
	extra := map[string]any{}
	for field, value := range opts {
		if emptyScalar(value) {
			continue
		}
		if param := xhttpFields[field]; param != "" {
			if download && field == "no-grpc-header" {
				param = "noGrpcHeader"
			}
			extra[param] = value
			continue
		}
		switch field {
		case "path", "host", "mode":
			if download {
				extra[field] = value
			}
		case "headers":
			if !download {
				return nil, errors.New("URI cannot preserve XHTTP uplink headers")
			}
			extra[field] = value
		case "reuse-settings":
			rs, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid XHTTP reuse settings")
			}
			xmux := map[string]any{}
			for k, v := range rs {
				param := reuseFields[k]
				if param == "" {
					return nil, fmt.Errorf("URI cannot preserve XHTTP reuse %q", k)
				}
				xmux[param] = v
			}
			if download {
				extra["extra"] = map[string]any{"xmux": xmux}
			} else {
				extra["xmux"] = xmux
			}
		case "download-settings":
			if download {
				return nil, errors.New("nested XHTTP download settings")
			}
			ds, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid XHTTP download settings")
			}
			transport := map[string]any{}
			downloadSettings := map[string]any{}
			tlsSettings := map[string]any{}
			for k, v := range ds {
				switch k {
				case "server":
					downloadSettings["address"] = v
				case "port":
					downloadSettings["port"] = v
				case "tls":
					if v == true {
						downloadSettings["security"] = "tls"
					}
				case "servername":
					tlsSettings["serverName"] = v
				case "client-fingerprint":
					tlsSettings["fingerprint"] = v
				case "alpn":
					tlsSettings["alpn"] = v
				case "skip-cert-verify":
					tlsSettings["allowInsecure"] = v
				case "reality-opts":
					r, ok := v.(map[string]any)
					if !ok {
						return nil, errors.New("invalid XHTTP Reality")
					}
					if err := uriFields(r, "public-key short-id"); err != nil {
						return nil, err
					}
					downloadSettings["security"] = "reality"
					downloadSettings["realitySettings"] = map[string]any{"publicKey": r["public-key"], "shortId": r["short-id"]}
				default:
					transport[k] = v
				}
			}
			t, err := encodeXHTTPExtra(transport, true)
			if err != nil {
				return nil, err
			}
			if len(t) > 0 {
				downloadSettings["xhttpSettings"] = t
			}
			if len(tlsSettings) > 0 {
				downloadSettings["tlsSettings"] = tlsSettings
			}
			extra["downloadSettings"] = downloadSettings
		default:
			return nil, fmt.Errorf("URI cannot preserve XHTTP field %q", field)
		}
	}
	return extra, nil
}

func decodeURITransport(q url.Values, c map[string]any) error {
	network := q.Get("type")
	if network == "tcp" && q.Get("headerType") == "http" {
		network = "http"
	}
	if network == "" || network == "tcp" {
		return nil
	}
	for _, field := range []string{"ws-opts", "grpc-opts", "http-opts", "h2-opts", "xhttp-opts"} {
		delete(c, field)
	}
	c["network"] = network
	path := q.Get("path")
	if path == "" {
		path = "/"
	}
	host := q.Get("host")
	switch network {
	case "ws", "httpupgrade":
		opts := map[string]any{"path": path}
		if host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		if network == "httpupgrade" {
			c["network"] = "ws"
			opts["v2ray-http-upgrade"] = true
		}
		if v := q.Get("ed"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return errors.New("invalid early data size")
			}
			if network == "httpupgrade" {
				opts["v2ray-http-upgrade-fast-open"] = n > 0
			} else {
				opts["max-early-data"] = n
				opts["early-data-header-name"] = "Sec-WebSocket-Protocol"
			}
		}
		if v := q.Get("eh"); v != "" {
			opts["early-data-header-name"] = v
		}
		c["ws-opts"] = opts
	case "grpc":
		service := q.Get("serviceName")
		if service == "" {
			service = q.Get("path")
		}
		opts := map[string]any{"grpc-service-name": service}
		if q.Get("grpcUserAgent") != "" {
			opts["grpc-user-agent"] = q.Get("grpcUserAgent")
		}
		c["grpc-opts"] = opts
	case "http":
		opts := map[string]any{"path": []string{path}}
		if host != "" {
			opts["headers"] = map[string]any{"Host": strings.Split(host, ",")}
		}
		if q.Get("method") != "" {
			opts["method"] = q.Get("method")
		}
		c["http-opts"] = opts
	case "h2":
		opts := map[string]any{"path": path}
		if host != "" {
			opts["host"] = strings.Split(host, ",")
		}
		c["h2-opts"] = opts
	case "xhttp":
		opts := map[string]any{}
		for _, key := range []string{"path", "host", "mode"} {
			if q.Get(key) != "" {
				opts[key] = q.Get(key)
			}
		}
		if q.Get("extra") != "" {
			var extra map[string]any
			if json.Unmarshal([]byte(q.Get("extra")), &extra) != nil {
				return errors.New("invalid XHTTP extra JSON")
			}
			if err := decodeXHTTPExtra(extra, opts, false); err != nil {
				return err
			}
		}
		c["xhttp-opts"] = opts
	default:
		return fmt.Errorf("unsupported URI transport %q", network)
	}
	return nil
}
