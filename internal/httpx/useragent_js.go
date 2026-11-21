//go:build js

package httpx

// sendUserAgent is off in a browser: the browser identifies itself, and a
// User-Agent header of our own would make it ask every upstream for
// permission first (a CORS preflight), which the planners do not answer.
const sendUserAgent = false
