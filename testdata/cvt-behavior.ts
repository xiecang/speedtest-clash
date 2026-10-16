// Development-only behavioral reference. Run with the user's local cvt tree.
const root = Deno.args[0];
if (!root) throw new Error('usage: deno run --allow-read testdata/cvt-behavior.ts /path/to/cvt');
const {fromURI} = await import(`file://${root}/netlify/edge-functions/main/uris.ts`);
const inputs = [
 'http://user:password@example.invalid:8080#http',
 'https://user:password@example.invalid:443#https',
 'trojan://password@example.invalid:443?sni=example.invalid#trojan',
 'hysteria2://password@example.invalid:443?sni=example.invalid#hy2',
 'vless://e28783bb-5a3f-4b0b-8472-692fa8f9f51b@example.invalid:443?security=tls&type=ws&path=%2Fchat&sni=example.invalid#vless',
];
for (const uri of inputs) console.log(JSON.stringify({uri,config:fromURI(uri)}));
