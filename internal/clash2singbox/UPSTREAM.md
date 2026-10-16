# Imported conversion code

Source: https://github.com/xmdhs/clash2singbox
Commit: `355e710719d099c629ca4502572870167de5bcd9`
License: MIT; original copyright and full license: `../../../LICENSES/clash2singbox-MIT.txt`.

Only `convert/` and `model/` were copied; the upstream loaders, server and CLI were not imported. Some upstream template/insecure helper functions remain internal for provenance and upstream tests. The V2 production path only calls `Clash2sing`; it never calls `ToInsecure` or template generators.

Local changes: package imports, sing-box 1.14 target enum, port hopping without simultaneous fixed server_port, gRPC transport with an empty service name, explicit Reality TLS enablement, WireGuard port/MTU bounds, and canonical WireGuard allowed-ips with the legacy spelling accepted. Root V2 code adds strict field diagnostics, native config validation, stable path identities, and new DNS/endpoints syntax. Upstream converter tests remain active.
