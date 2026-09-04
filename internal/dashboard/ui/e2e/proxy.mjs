// Stands in for `tailscale serve`: forwards to the daemon on loopback and
// attaches the identity header, stripping any the client supplied. That
// stripping is the property the real proxy provides and the whole reason the
// header can be trusted, so the fixture has to model it rather than just add
// a header.
import { createServer, request } from 'node:http';
import { DAEMON_PORT, PROXY_PORT, VIEWER_LOGIN } from './fixture.mjs';

const login = process.env.E2E_VIEWER ?? VIEWER_LOGIN;

createServer((req, res) => {
  const headers = { ...req.headers };
  delete headers['tailscale-user-login'];
  delete headers.host;
  if (login) headers['tailscale-user-login'] = login;

  const up = request({ host: '127.0.0.1', port: DAEMON_PORT, path: req.url, method: req.method, headers }, (r) => {
    res.writeHead(r.statusCode ?? 502, r.headers);
    r.pipe(res);
  });
  up.on('error', () => { res.writeHead(502); res.end('upstream unavailable'); });
  req.pipe(up);
}).listen(PROXY_PORT, '127.0.0.1', () => console.log(`proxy on ${PROXY_PORT} as ${login || '(anonymous)'}`));
