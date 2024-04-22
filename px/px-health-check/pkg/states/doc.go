// This package was created to move Kubernetes and Portworx client setup away
// from pkg/healthcheck . This will allow for the healthcheck package to be
// used in other projects without the need to vendor in the Kubernetes and
// Portworx client-go packages.
package states
