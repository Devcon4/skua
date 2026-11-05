import type {Server} from 'node:http';
import type {AddressInfo} from 'node:net';

export async function listenOnLoopback(
  server: Server,
  port: number | undefined,
): Promise<string> {
  const listening = Promise.withResolvers<undefined>();
  server.once('error', listening.reject);
  server.listen(port ?? 0, '127.0.0.1', () => {
    listening.resolve(undefined);
  });
  await listening.promise;
  const {port: bound} = server.address() as AddressInfo;
  return `http://127.0.0.1:${String(bound)}`;
}

export async function closeServer(server: Server): Promise<void> {
  server.closeAllConnections();
  const closed = Promise.withResolvers<undefined>();
  server.close(() => {
    closed.resolve(undefined);
  });
  await closed.promise;
}
