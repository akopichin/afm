import net from 'node:net';
import { bridge, parseLaunch, readBroker } from './companion.js';

const launch = parseLaunch(process.argv.slice(2));
if (!launch.broker || !launch.profile) {
  process.stderr.write('AFM Desktop stage browser: missing broker file or profile directory.\n');
  process.exit(2);
}
let broker;
try { broker = readBroker(launch.broker); }
catch (error) { process.stderr.write('AFM Desktop is not running: ' + error.message + '\n'); process.exit(1); }
const input = new net.Socket({ fd: 3, readable: true, writable: false });
const output = new net.Socket({ fd: 4, readable: false, writable: true });
process.exit(await bridge({ launch, broker, input, output }));
