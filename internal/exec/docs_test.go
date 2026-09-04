package exec

import "testing"

func TestSplitJoinReplace(t *testing.T) {
	text := "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: cart\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: cart-api\n  namespace: cart\nspec:\n  replicas: 1\n"
	s := SplitDocs(text)
	if len(s.Docs) != 2 || s.Docs[0].Type != "v1/Namespace" || s.Docs[0].Name != "/cart" || s.Docs[1].Name != "cart/cart-api" {
		t.Fatalf("docs: %+v", s.Docs)
	}
	if s.Join() != text {
		t.Errorf("join is not byte-exact:\n%q", s.Join())
	}
	i, err := s.Find("apps/v1/Deployment", "cart/cart-api")
	if err != nil || i != 1 {
		t.Fatalf("find: %d %v", i, err)
	}
	out := s.Replace(1, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: cart-api\n  namespace: cart\nspec:\n  replicas: 3\n")
	if out != "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: cart\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: cart-api\n  namespace: cart\nspec:\n  replicas: 3\n" {
		t.Errorf("replace:\n%s", out)
	}
	// No leading separator, single doc.
	one := SplitDocs("kind: ConfigMap\napiVersion: v1\nmetadata:\n  name: c\n")
	if len(one.Docs) != 1 || one.Docs[0].Type != "v1/ConfigMap" || one.Join() != "kind: ConfigMap\napiVersion: v1\nmetadata:\n  name: c\n" {
		t.Errorf("single: %+v", one.Docs)
	}
}
