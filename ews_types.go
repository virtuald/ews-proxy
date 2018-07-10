/*
	Types used by ews_data.go, with helper methods
*/

package ews

import (
	"encoding/xml"
	"strings"

	"github.com/pkg/errors"
)

// simple type definitions
const (
	T_BOOL = iota
	T_NUM  = iota
	T_STR  = iota
	T_ENUM = iota
	T_LIST = iota
)

// element is used for initialization only
type element struct {
	XN string // XML element name
	JN string // JSON key name (because sometimes they're not the same)
	T  string // typename in ews_types
	JT string // if specified, a specific json type hint for this element
	// -> used when multiple elements have the same json type
	List        bool
	JsonDefault interface{}
}

// EwsXmlElement is used for XML -> JSON conversion
type EwsXmlElement struct {
	JsonName string
	Type     *EwsType
	IsList   bool
}

type EwsXmlJsonDefault struct {
	JsonName    string
	JsonDefault interface{}
}

// EwsJsonElement is used for JSON -> XML conversion
type EwsJsonElement struct {
	JsonName string

	// key is the json type
	Types map[string]*EwsJsonType
	// key is the xml local element name (only used in hooks)
	Elements map[string]*EwsJsonType

	SingleType *EwsJsonType
	IsList     bool

	// used to determine which type should be used
	XmlChoiceHook XmlChoiceFunc
}

type EwsJsonType struct {
	Type   *EwsType
	XmlTag xml.Name

	// only used for initialization
	jsonType string
}

type EwsType struct {
	Name string

	// used to compute Elements and JsonElementList
	elements   []element
	Attributes []element

	// lookup for XML -> JSON
	TypeByElementName map[string]*EwsXmlElement
	JsonDefaults      []EwsXmlJsonDefault

	// lookup for JSON -> XML
	// -> list is for ordering, map is for when type lookup is required
	JsonElementList []*EwsJsonElement
	JsonExtra       []string

	// XML attributes, key is XmlName
	Attrs map[string]*EwsType
	// key is XmlName, value is JsonName
	AttrsNames map[string]string

	AnyAttr    bool
	IsSimple   bool
	SimpleType int
	TextAttr   string
	JsonType   string

	IsList          bool
	JsonListName    string
	JsonListElement *EwsJsonElement // only set if JsonListName/IsList is
	JsonHook        JsonHookFunc    // only set if special function is needed

	EnumValues []string // if this is an eumeration, these are the values

	ListItemTypeStr string   // the type of the listObj
	ListItemType    *EwsType // ListItemTypeStr converted to a type
}

type OpDescriptor struct {
	Action   string
	Request  *EwsType
	Response EwsJsonElement

	BodyType    string
	RequestType string
}

func (v *EwsType) Initialize() {

	// given the initial set of element data, build data structures that
	// make it easy to translate stuff

	// create Attrs
	v.Attrs = make(map[string]*EwsType)
	v.AttrsNames = make(map[string]string)

	for i, a := range v.Attributes {
		t := ewsTypes[a.T]
		v.Attrs[a.XN] = t
		if a.JN == "" {
			v.Attributes[i].JN = a.XN
			a.JN = a.XN
		}
		v.AttrsNames[a.XN] = a.JN
	}

	v.TypeByElementName = make(map[string]*EwsXmlElement)
	tmp := make(map[string]*EwsJsonElement)

	// iterate the elements and construct
	for _, e := range v.elements {

		ename := strings.Split(e.XN, ":")[1]
		jname := ename
		if e.JN != "" {
			jname = e.JN
		}

		t := ewsTypes[e.T]

		// stuff needed for xml -> JSON is easy, start here
		v.TypeByElementName[ename] = &EwsXmlElement{
			JsonName: jname,
			Type:     t,
			IsList:   e.List,
		}

		if e.JsonDefault != nil {
			v.JsonDefaults = append(v.JsonDefaults, EwsXmlJsonDefault{JsonName: jname, JsonDefault: e.JsonDefault})
		}

		// JSON -> XML is harder, collect duplicate json names together
		je := tmp[jname]
		if je == nil {
			je = NewEwsJsonElement(v.Name, jname, e.List)
			v.JsonElementList = append(v.JsonElementList, je)
			tmp[jname] = je
		}

		jt := &EwsJsonType{
			Type:     t,
			XmlTag:   xml.Name{Local: e.XN},
			jsonType: e.JT,
		}

		// HACK for ArrayOfResponseMessagesType
		if v.Name == "ArrayOfResponseMessagesType" {
			if je.Types == nil {
				je.Types = make(map[string]*EwsJsonType)
			}
			je.Types[ename] = jt
		} else {
			je.add(jt)
		}
	}

	if v.JsonListName != "" || v.IsList {
		je := NewEwsJsonElement(v.Name, v.JsonListName, true)

		// hack
		if v.Name == "ArrayOfResponseMessagesType" {
			je.Types = v.JsonElementList[0].Types
		} else {
			// add all element children to this thing
			for _, jee := range v.JsonElementList {
				if jee.SingleType != nil {
					je.add(jee.SingleType)
				} else {
					for _, v := range jee.Types {
						je.add(v)
					}
				}
			}
		}

		v.JsonListElement = je
	}

	// insert the json hook
	v.JsonHook = jsonHooks[v.Name]

	// resolve ListItemTypeStr to ListItemType
	if v.ListItemTypeStr != "" {
		if itemType, ok := ewsTypes[v.ListItemTypeStr]; ok {
			v.ListItemType = itemType
		} else {
			panic("Internal error, cannot find list item type: " + v.ListItemTypeStr + " for " + v.Name)
		}
	}
}

func NewEwsJsonType(xmlLocal string, typ *EwsType) *EwsJsonType {
	return &EwsJsonType{XmlTag: xml.Name{Local: xmlLocal}, Type: typ}
}

func (this *EwsJsonType) EmitStart(enc *xml.Encoder, attrs []xml.Attr) error {
	return enc.EncodeToken(xml.StartElement{Name: this.XmlTag, Attr: attrs})
}

func (this *EwsJsonType) EmitEnd(enc *xml.Encoder) error {
	return enc.EncodeToken(xml.EndElement{Name: this.XmlTag})
}

func NewEwsJsonElement(xmlName string, jsonName string, isList bool) *EwsJsonElement {
	return &EwsJsonElement{
		JsonName:      jsonName,
		IsList:        isList,
		XmlChoiceHook: xmlChoiceHooks[xmlName],
		Elements:      make(map[string]*EwsJsonType),
	}
}

func (e *EwsJsonElement) add(jt *EwsJsonType) {
	if e.SingleType != nil {
		e.Types = make(map[string]*EwsJsonType)
		e.addInternal(e.SingleType)
		e.SingleType = nil
	}

	// if there are already multiple types
	if e.Types != nil {
		e.addInternal(jt)
	} else {
		e.SingleType = jt
	}

	e.Elements[jt.XmlTag.Local] = jt
}

func (e *EwsJsonElement) addInternal(jt *EwsJsonType) {

	var types []string
	if jt.jsonType == "" {
		// sometimes ews changes the type too... so put both types in
		s := strings.Split(jt.Type.JsonType, ":")
		types = []string{jt.Type.JsonType, s[0] + "Type" + ":#Exchange"}
	} else {
		types = []string{jt.jsonType}
	}

	for _, t := range types {
		// TODO: this could cause decoding errors
		//if e.Types[t] != nil {
		//	fmt.Printf("duplicate type!? %s -- %s // %#v\n", e.JsonName, t, jt)
		//}
		e.Types[t] = jt
	}
}

func (e *EwsJsonElement) IsCharData() bool {
	return e.Types == nil &&
		(e.SingleType == nil ||
			(e.SingleType.Type.IsSimple && e.SingleType.Type.TextAttr == ""))
}

//
// two types of hooks present
// - JsonHookFunc: modifies JSON that was created from SOAP XML
// - XmlChoiceFunc: chooses the EwsType based on the JSON contents
//

type JsonHookFunc func(*EwsType, *OrderedObject)
type XmlChoiceFunc func(*EwsJsonElement, map[string]interface{}) (*EwsJsonType, error)

var jsonHooks = map[string]JsonHookFunc{

	"ResolveNamesType": func(t *EwsType, obj *OrderedObject) {
		_, exists := obj.Get("ContactDataShape")
		if !exists {
			obj.Set("ContactDataShape", "Default")
		}
	},
}

var xmlChoiceHooks = map[string]XmlChoiceFunc{

	"SyncFolderHierarchyChangesType": func(edesc *EwsJsonElement, element map[string]interface{}) (*EwsJsonType, error) {
		if changeType, ok := element["ChangeType"].(string); ok {
			changeType = "t:" + changeType
			typ := edesc.Elements[changeType]
			if typ != nil {
				return typ, nil
			}
		}

		return nil, errors.Errorf("Invalid ChangeType %#v %#v", element["ChangeType"], edesc)
	},
}
