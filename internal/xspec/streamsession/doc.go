// Package streamsession adapts production provider retry and conversation
// session transitions to the private Quint replay protocol. It injects fake
// clock, jitter, and provider outcomes; it does not carry a second retry or
// session policy implementation.
package streamsession
