// Package ratelimit limits request rates per client.
//
// It holds one in-memory token bucket per key. That is deliberate for phase 11:
// a single API process per environment makes an in-process bucket exact, and a
// shared Redis bucket is a phase 12 concern once the API runs more than one
// replica. Daily quotas are separate and live in the database so they survive a
// restart.
package ratelimit
