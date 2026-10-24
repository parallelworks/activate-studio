/**
 * The server's first import (main.ts), so every module after it, and every
 * request they make, sees the system's trusted certificates and the proxy
 * (netTrust.ts).
 */
import { outboundLine, trustSystemCertificates, useProxyFromEnv } from './netTrust.js'

trustSystemCertificates()
useProxyFromEnv()
console.log(outboundLine())
