// Package serviceauth authenticates supplier-service's outgoing
// service-to-service calls (NFR3.3, NFR3.5.2, NFR3.5.3).
//
// ClientCredentials is a TokenSource that obtains an Auth0
// client-credentials token for one audience, caches it until shortly before
// it expires and shares one fetch between concurrent callers. NewTransport
// adds the token to every request as a bearer Authorization header.
//
// This is the outgoing half of order-service's package of the same name.
// The client secret comes only from Config. Neither the secret nor any token
// is logged or included in an error message.
package serviceauth
