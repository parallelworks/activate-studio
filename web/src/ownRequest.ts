/** The header the Studio's page sends with its requests: a page of another
 *  site cannot send one without a CORS preflight, which the Studio never
 *  grants, so the server knows its own page by it where the browser does not
 *  say (plain http on a host name) and a proxy rewrites Host
 *  (server-go/internal/libapi/pwbucket.go, otherSite). */
export const OWN_REQUEST = { 'X-Studio-Request': '1' }
