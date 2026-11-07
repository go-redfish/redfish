package redfish

import (
	"context"
	"errors"
)

// $expand in the read paths (DSP0266 7.3.2). The inventories read a collection
// and then each of its members: N+1 requests. When the service root advertises
// ExpandQuery.NoLinks (the period) in ProtocolFeaturesSupported, one
// GET collection?$expand=. answers with the members inline ("a client can request
// multiple SoftwareInventory collection member resources in one request rather
// than fetching them one at a time"). Rules that keep the answer the one the
// per-member reads give:
//
//   - the parameter is sent only when it is advertised and Config.NoExpand is
//     not set; ProtocolFeaturesSupported is read once per Client;
//   - a member the service did not expand (it answers a link: 7.3.2 "Return the
//     @odata.id property for the member that it cannot expand") is read on its
//     own;
//   - any failure of the expanded request (501 or 400 from a service that
//     advertises more than it does, 507 for a payload too large, an answer over
//     MaxExpandedBody) makes the read start again member by member;
//   - a paged collection (Members@odata.nextLink, 7.2.2) is followed, and the
//     parameter is added to a next link that lost it (9.6.12 only says it
//     "should" carry it).

// member is one member resource of a collection.
type member struct {
	URI   string
	Props Props
}

// canExpand reports whether the collections may be read with $expand=.
func (c *Client) canExpand(ctx context.Context) bool {
	if c.cfg.NoExpand {
		return false
	}
	f, err := c.ProtocolFeatures(ctx)
	return err == nil && f.ExpandNoLinks
}

// memberResources reads the members of a collection: at most max over at most
// pages pages, each as the resource it is. tolerate, when it is not nil, says
// which read error leaves a member out; any other error ends the read.
func (c *Client) memberResources(ctx context.Context, coll string, max, pages int, tolerate func(error) bool) ([]member, error) {
	if c.canExpand(ctx) {
		if out, ok, err := c.expandedMembers(ctx, coll, max, pages, tolerate); ok || err != nil {
			return out, err
		}
	}
	uris, err := c.membersMax(ctx, coll, max, pages)
	if err != nil {
		return nil, err
	}
	out := make([]member, 0, len(uris))
	for _, u := range uris {
		m, _, err := c.getProps(ctx, u)
		if err != nil {
			if tolerate != nil && tolerate(err) {
				continue
			}
			return nil, err
		}
		out = append(out, member{u, m})
	}
	return out, nil
}

// expandedMembers is memberResources with $expand=. ok is false when the
// expanded read failed and the caller must read member by member.
func (c *Client) expandedMembers(ctx context.Context, coll string, max, pages int, tolerate func(error) bool) (out []member, ok bool, err error) {
	var got []member
	var todo []int // indexes of members that came as links
	n := 0
	next := withExpand(coll)
	for page := 0; next != "" && page < pages && n < max; page++ {
		r, err := c.Get(withLimit(ctx), next)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			return nil, false, nil
		}
		p, derr := decodeProps(r.Body)
		if derr != nil {
			return nil, false, nil
		}
		list, _ := p["Members"].([]any)
		if list == nil {
			list, _ = sub(p, "Links")["Members"].([]any)
		}
		for _, x := range list {
			m, _ := x.(map[string]any)
			id, _ := m["@odata.id"].(string)
			if servicePath(id) == "" || n >= max {
				continue
			}
			n++
			got = append(got, member{servicePath(id), m})
			if len(m) <= 1 { // a link only: not expanded
				todo = append(todo, len(got)-1)
			}
		}
		next = servicePath(str(p, "Members@odata.nextLink"))
		if next != "" {
			next = withExpand(next)
		}
	}
	drop := map[int]bool{}
	for _, i := range todo {
		m, _, err := c.getProps(ctx, got[i].URI)
		if err != nil {
			if tolerate != nil && tolerate(err) && !errors.Is(err, context.Canceled) {
				drop[i] = true
				continue
			}
			return nil, true, err
		}
		got[i].Props = m
	}
	out = make([]member, 0, len(got))
	for i, m := range got {
		if !drop[i] {
			out = append(out, m)
		}
	}
	return out, true, nil
}

// notAuth tolerates every read error but a refusal of the credentials.
func notAuth(err error) bool { return !errors.Is(err, ErrAuth) }
