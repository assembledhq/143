package main

import (
	"strings"
	"unicode"
)

type category string

const (
	categoryEmail         category = "an email address"
	categoryPersonName    category = "a person's name"
	categoryAccountHandle category = "a username or login"
	categoryIPAddress     category = "an IP address"
	categoryUserAgent     category = "a user agent"
	categoryPhone         category = "a phone number"
	categoryPostalAddress category = "a postal address"
	categorySensitiveID   category = "a government, financial, or birth-date identifier"
	categoryUserRecord    category = "a whole user record"
	categoryHeaders       category = "full request headers"
)

var nameModifiers = set(
	"first", "last", "given", "family", "middle", "sur", "nick", "display", "real", "legal", "preferred",
)

var personRoles = set(
	"user", "author", "sender", "recipient", "customer", "person", "contact", "assignee", "reviewer",
	"requester", "member", "committer", "creator", "invitee", "inviter", "employee", "reporter",
)

var singleTokenPersonNames = set(
	"firstname", "lastname", "fullname", "displayname", "realname", "givenname", "familyname",
	"surname", "nickname",
)

var handleQualifiers = set("user", "slack", "github", "twitter", "social", "member")

var clientAddrQualifiers = set("remote", "client", "visitor", "caller", "origin", "requester", "source", "real")

var addressQualifiers = set(
	"street", "home", "mailing", "postal", "billing", "shipping", "physical", "residential",
)

var wholeUserRecords = set(
	"user", "users", "member", "members", "invitee", "invitees", "profile", "profiles", "contact",
	"contacts", "customer", "customers", "person", "people", "author", "sender", "recipient",
	"requester", "assignee", "committer",
)

var clientIPHeaders = set(
	"x-forwarded-for", "x-real-ip", "forwarded", "cf-connecting-ip", "true-client-ip", "x-client-ip",
	"fastly-client-ip", "x-cluster-client-ip",
)

// classifyName reports which kind of personal data an identifier, field key,
// or header name refers to. It looks at the head noun — the last word — so
// "user_email" and "githubLogin" match while "email_domain", "login_method",
// and "slack_user_id" (metadata about, or a reference to, the data) do not.
func classifyName(name string) (category, bool) {
	lower := strings.ToLower(name)
	if clientIPHeaders[lower] {
		return categoryIPAddress, true
	}
	if lower == "user-agent" {
		return categoryUserAgent, true
	}

	words := splitWords(name)
	n := len(words)
	if n == 0 {
		return "", false
	}
	last := words[n-1]
	prev := ""
	if n > 1 {
		prev = words[n-2]
	}
	prevPrev := ""
	if n > 2 {
		prevPrev = words[n-3]
	}

	switch {
	case last == "email" || last == "emails" || last == "emailaddress" || last == "emailaddresses":
		return categoryEmail, true
	case prev == "email" && (last == "address" || last == "addresses" || last == "addr"):
		return categoryEmail, true

	case last == "phone" || last == "phones" || last == "telephone" || last == "msisdn" || last == "phonenumber":
		return categoryPhone, true
	case prev == "phone" && (last == "number" || last == "numbers"):
		return categoryPhone, true
	case prev == "mobile" && (last == "number" || last == "numbers"):
		return categoryPhone, true

	case singleTokenPersonNames[last]:
		return categoryPersonName, true
	case (last == "name" || last == "names") && (nameModifiers[prev] || personRoles[prev]):
		return categoryPersonName, true
	case (last == "name" || last == "names") && prev == "full" && personRoles[prevPrev]:
		return categoryPersonName, true

	case last == "login" || last == "logins" || last == "username" || last == "usernames" || last == "screenname":
		return categoryAccountHandle, true
	case prev == "screen" && last == "name":
		return categoryAccountHandle, true
	case (last == "handle" || last == "handles") && handleQualifiers[prev]:
		return categoryAccountHandle, true

	case last == "useragent" || (prev == "user" && last == "agent"):
		return categoryUserAgent, true

	case (last == "ip" || last == "ips" || last == "ipaddr" || last == "ipaddress") && (prev == "" || clientAddrQualifiers[prev]):
		return categoryIPAddress, true
	case prev == "ip" && (last == "address" || last == "addresses" || last == "addr") && (prevPrev == "" || clientAddrQualifiers[prevPrev]):
		return categoryIPAddress, true
	case clientAddrQualifiers[prev] && (last == "addr" || last == "address" || last == "ip" || last == "ips"):
		return categoryIPAddress, true
	case prev == "forwarded" && last == "for":
		return categoryIPAddress, true

	case addressQualifiers[prev] && (last == "address" || last == "addresses"):
		return categoryPostalAddress, true
	case last == "street" || last == "zipcode" || last == "postcode" || last == "postalcode":
		return categoryPostalAddress, true
	case (prev == "postal" || prev == "zip" || prev == "post") && last == "code":
		return categoryPostalAddress, true

	case last == "ssn" || last == "passport" || last == "dob" || last == "birthdate" || last == "birthday" || last == "iban":
		return categorySensitiveID, true
	case prevPrev == "social" && prev == "security" && last == "number":
		return categorySensitiveID, true
	case prevPrev == "date" && prev == "of" && last == "birth":
		return categorySensitiveID, true
	case prev == "national" && (last == "id" || last == "number"):
		return categorySensitiveID, true
	case (prev == "card" && last == "number") || (prev == "credit" && last == "card"):
		return categorySensitiveID, true
	}
	return "", false
}

// classifyRecord reports whether an identifier names a value that holds
// personal data in several fields at once — a user, member, or invitee record,
// or a request's header map (cookies, user agent, forwarded client IP) — and
// therefore leaks when serialized whole.
func classifyRecord(name string) (category, bool) {
	words := splitWords(name)
	if len(words) == 0 {
		return "", false
	}
	switch last := words[len(words)-1]; {
	case wholeUserRecords[last]:
		return categoryUserRecord, true
	case last == "header" || last == "headers":
		return categoryHeaders, true
	}
	return "", false
}

// splitWords lowercases an identifier and splits it on underscores, dashes,
// dots, spaces, and camelCase boundaries, keeping acronyms together:
// "XForwardedFor" → [x forwarded for], "remoteIPAddr" → [remote ip addr].
func splitWords(name string) []string {
	var words []string
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		runes := []rune(part)
		start := 0
		for i := 1; i < len(runes); i++ {
			lowerToUpper := unicode.IsLower(runes[i-1]) && unicode.IsUpper(runes[i])
			acronymEnd := unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i]) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if lowerToUpper || acronymEnd {
				words = append(words, strings.ToLower(string(runes[start:i])))
				start = i
			}
		}
		words = append(words, strings.ToLower(string(runes[start:])))
	}
	return words
}

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}
