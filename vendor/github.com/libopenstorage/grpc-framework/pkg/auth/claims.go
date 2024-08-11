package auth

import "fmt"

// UsernameClaimType holds the claims type to be used as the unique id for the user
type UsernameClaimType string

const (
	// default type is sub
	UsernameClaimTypeDefault UsernameClaimType = ""
	// UsernameClaimTypeSubject requests to use "sub" as the claims for the
	// ID of the user
	UsernameClaimTypeSubject UsernameClaimType = "sub"
	// UsernameClaimTypeEmail requests to use "name" as the claims for the
	// ID of the user
	UsernameClaimTypeEmail UsernameClaimType = "email"
	// UsernameClaimTypeName requests to use "name" as the claims for the
	// ID of the user
	UsernameClaimTypeName UsernameClaimType = "name"
)

var (
	// Required claim keys
	requiredClaims = []string{"iss", "sub", "exp", "iat", "name", "email"}
	// Custom claims for OpenStorage
	customClaims = []string{"roles", "groups"}
)

// Claims provides information about the claims in the token
// See https://openid.net/specs/openid-connect-core-1_0.html#IDToken
// for more information.
type Claims struct {
	// Issuer is the token issuer. For selfsigned token do not prefix
	// with `https://`.
	Issuer string `json:"iss"`
	// Subject identifier. Unique ID of this account
	Subject string `json:"sub" yaml:"sub"`
	// Account name
	Name string `json:"name" yaml:"name"`
	// Account email
	Email string `json:"email" yaml:"email"`
	// Audience is the intended audience for this claim. Can be a string or []string or []interface{}.
	// Use GetAudience() to interpret the value correctly.
	Audience interface{} `json:"aud,omitempty" yaml:"aud,omitempty"`
	// Roles of this account
	Roles []string `json:"roles,omitempty" yaml:"roles,omitempty"`
	// (optional) Groups in which this account is part of
	Groups []string `json:"groups,omitempty" yaml:"groups,omitempty"`
	// UsernameClaim indicates which claim has the user name. It should be set by the authenticator when
	// authenticating the raw token.
	UsernameClaim UsernameClaimType `json:"usernameClaim,omitempty" yaml:"usernameClaim,omitempty"`
}

// GetAudience returns the audience from the claims
func (c *Claims) GetAudience() ([]string, error) {
	var ret []string
	if c.Audience == nil {
		return ret, nil
	} else if aud, ok := c.Audience.(string); ok {
		ret = append(ret, aud)
	} else if aud, ok := c.Audience.([]string); ok {
		ret = append(ret, aud...)
	} else if aud, ok := c.Audience.([]interface{}); ok {
		for _, a := range aud {
			vs, ok := a.(string)
			if !ok {
				return nil, fmt.Errorf("unknown type %T for audience entry %v in audience %v", a, a, c.Audience)
			}
			ret = append(ret, vs)
		}
	} else {
		return nil, fmt.Errorf("unknown type %T for audience %v", c.Audience, c.Audience)
	}
	return ret, nil
}

// GetUsername returns the username from the claims
func (c *Claims) GetUsername() (string, error) {
	username := ""
	claimType := c.UsernameClaim
	if claimType == "" {
		claimType = UsernameClaimTypeSubject
	}
	switch claimType {
	case UsernameClaimTypeEmail:
		username = c.Email
	case UsernameClaimTypeName:
		username = c.Name
	case UsernameClaimTypeSubject:
		username = c.Subject
	default:
		return "", fmt.Errorf("system set to use unknown claim %s as username. Must be one of %s, %s or %s (default)",
			claimType, UsernameClaimTypeEmail, UsernameClaimTypeName, UsernameClaimTypeSubject)
	}
	if username == "" {
		return "", fmt.Errorf("system set to use the value of %s as the username,"+
			" therefore the value of %s in the token cannot be empty", claimType, claimType)

	}
	return username, nil
}

// ValidateUsername validates that the claim that is suppposed to contain the username is present
func (c *Claims) ValidateUsername() error {
	_, err := c.GetUsername()
	return err
}
