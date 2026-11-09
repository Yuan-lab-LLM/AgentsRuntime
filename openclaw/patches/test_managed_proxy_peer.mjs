// Disposable test-network relay; never publish these ports on the host.
import net from 'node:net';
for (let port = 20000; port <= 20200; port++) {
  net.createServer(incoming => {
    const outgoing = net.connect({ host: 'proxy-startup-runtime', port });
    incoming.on('error', () => outgoing.destroy());
    outgoing.on('error', () => incoming.destroy());
    incoming.pipe(outgoing).pipe(incoming);
  }).listen(port, '0.0.0.0');
}
