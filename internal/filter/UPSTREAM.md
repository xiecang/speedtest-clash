# Filter runtime provenance

`assets/filter.js` is the JavaScript translation of the local cvt `netlify/edge-functions/main/filter.ts` at commit `7c8c6f6f078ebf888c3694f1c22364d2485b9af1`, produced with Node 24 `stripTypeScriptTypes({mode:"transform"})`. The trailing compile/match helpers cache at most 256 expressions. No cvt deployment wrapper is imported.

`assets/quickjs.wasm` is copied unchanged from `github.com/fastschema/qjs@v0.0.6/qjs.wasm`. Only its fixed C ABI is used; the upstream Go bindings are not imported. The qjs and bundled QuickJS licenses are retained alongside the asset. Its QuickJS source gitlink is `d01ca4491fb24ccfeccb4c7394e28a3b21fd5986` in `https://github.com/quickjs-ng/quickjs`.

SHA-256:

- quickjs.wasm: `88fcbeead33cca3ec8394feb0fb3eec8047ec9ac5062aba7a63799cee11989b7`
- original cvt filter.ts: `cb03ac309095d168a098ccdd44c5f6c6aef42a679837a0b817848b229381fcab`

The Go bridge uses the project's pinned metacubex/wazero dependency, no CGO or external JS process. There are two warm machines, each capped at 64 MiB WASM memory and 32 MiB JS heap, with no filesystem/socket/environment capabilities. Each evaluation has a two-second deadline; failed or cancelled machines are destroyed rather than reused. Fixed-code compilation/initialization has a 60-second deadline to allow cold starts on instrumented or slow hosts; caller deadlines still apply. Node objects cross the boundary through JSON.parse, never executable object literals.
