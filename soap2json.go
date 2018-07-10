package ews

import (
	"encoding/xml"
	"io"
	//"log"
	"strings"

	"github.com/pkg/errors"
	"github.com/virtuald/go-ordered-json"
)

/*
	This converts Exchange's nasty JSON format to it's nasty SOAP XML format for
	interacting with the EWS API from Outlook Web Access. We provide a rewriter
	that can be used to provide access to SOAP clients when only the JSON endpoint
	is available.

	The exchange EWS/OWA endpoints are WCF services, and documentation on JSON/XML
	conversion in WCF can be found at

	* https://docs.microsoft.com/en-us/dotnet/framework/wcf/feature-details/stand-alone-json-serialization
	* https://docs.microsoft.com/en-us/dotnet/framework/wcf/feature-details/support-for-json-and-other-data-transfer-formats

	Important note about WCF -- it requires that the __type hint comes first in
	an object. Very annoying.
*/

// OrderedObject is only used for adding items, because json.OrderedObject is
// actually a slice
type OrderedObject struct {
	Object json.OrderedObject
	keys   map[string]int
}

func NewOrderedObject() *OrderedObject {
	return &OrderedObject{
		Object: make(json.OrderedObject, 0),
		keys:   make(map[string]int),
	}
}

func (obj *OrderedObject) Get(key string) (value interface{}, exists bool) {
	if idx, ok := obj.keys[key]; ok {
		return obj.Object[idx].Value, true
	} else {
		return nil, false
	}
}

// Set returns true if item added, false if it already exists
func (obj *OrderedObject) Set(key string, value interface{}) (exists bool) {
	if idx, ok := obj.keys[key]; ok {
		obj.Object[idx].Value = value
		return false
	} else {
		obj.keys[key] = len(obj.Object)
		obj.Object = append(obj.Object, json.Member{Key: key, Value: value})
		return true
	}
}

//
// XML -> JSON
//

func convertSimpleToJson(typ *EwsType, chardata string) (converted interface{}) {
	switch typ.SimpleType {
	case T_BOOL:
		if chardata == "true" || chardata == "1" {
			converted = true
		} else {
			converted = false
		}
	case T_NUM:
		converted = json.Number(chardata)
	case T_ENUM:
		// find chardata in enum_values
		for idx, value := range typ.EnumValues {
			if value == chardata {
				converted = idx
				return
			}
		}
		//LogError.Println("Cannot find ", chardata, " in ", typ.EnumValues, " using raw data instead")
		converted = chardata
	default:
		converted = chardata
	}
	return
}

func initRetObject(el xml.StartElement, typ *EwsType, simple bool) (obj *OrderedObject, listObj JsonList, ret interface{}, err error) {

	// if this isn't a simple type, then create a json object to
	// add elements to
	if !typ.IsSimple || typ.TextAttr != "" {
		if typ.JsonListName != "" || typ.IsList {
			for _, attr := range el.Attr {
				if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
					err = errors.Errorf("Type %s is list but has attributes!?", typ.Name)
					return
				}
			}

			listObj = make([]interface{}, 0)

			if typ.JsonListName != "" {
				obj = NewOrderedObject()

				// add my type too
				// .. hopefully WCF will just ignore extra type hints
				if typ.JsonType != "" {
					obj.Set("__type", typ.JsonType)
				}

				obj.Set(typ.JsonListName, listObj)
				ret = obj
			} else {
				ret = listObj
			}

		} else {
			obj = NewOrderedObject()
			ret = obj

			// add my type too
			// .. hopefully WCF will just ignore extra type hints
			if typ.JsonType != "" {
				obj.Set("__type", typ.JsonType)
			}

			// add my attributes to this
			for _, attr := range el.Attr {
				if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
					if atype, ok := typ.Attrs[attr.Name.Local]; ok {
						obj.Set(typ.AttrsNames[attr.Name.Local], convertSimpleToJson(atype, attr.Value))
					} else {
						err = errors.Errorf("Unknown attribute %s for type %s?", attr.Name.Local, typ.Name)
						return
					}
				}
			}
		}
	}

	return
}

// typ is never nil
func processElement(d *xml.Decoder, el xml.StartElement, typ *EwsType) (ret interface{}, err error) {

	var obj *OrderedObject
	var listObj []interface{}

	if typ == nil {
		err = errors.Errorf("No type in specification for %#v", el)
		return
	}

	// early attribute initialization
	if len(el.Attr) != 0 {
		if obj, listObj, ret, err = initRetObject(el, typ, false); err != nil {
			return
		}
	}

	for {
		var tok xml.Token
		tok, err = d.Token()
		if err != nil {
			return
		}

		switch tokel := tok.(type) {
		case xml.StartElement:

			if ret == nil {
				if obj, listObj, ret, err = initRetObject(el, typ, false); err != nil {
					return
				}
			}

			// look up the element type
			// -> this should always succeed
			nextElem, ok := typ.TypeByElementName[tokel.Name.Local]
			if !ok {
				return nil, errors.Errorf("unknown type %s", tokel.Name.Local)
			}

			jsonName := nextElem.JsonName

			var newItem interface{}
			newItem, err = processElement(d, tokel, nextElem.Type)
			if err != nil {
				return nil, err
			}

			//FIXME I think here is where we need to deal with enumerated lists, but we need a testcase
			if typ.JsonListName != "" {
				listObj = append(listObj, newItem)
				obj.Set(typ.JsonListName, listObj)

			} else if typ.IsList {
				listObj = append(listObj, newItem)
				ret = listObj

			} else if nextElem.IsList {
				if elistIf, ok := obj.Get(jsonName); ok {
					if elist, ok := elistIf.([]interface{}); ok {
						obj.Set(jsonName, append(elist, newItem))
					} else {
						return nil, errors.Errorf("Internal error: inconsistent list type")
					}
				} else {
					obj.Set(jsonName, []interface{}{newItem})
				}

			} else {
				// string hack
				if newItem == nil && nextElem.Type.IsSimple && nextElem.Type.SimpleType == T_STR {
					newItem = ""
				}

				if obj.Set(jsonName, newItem) == false {
					return nil, errors.Errorf("Internal error: collision on key %s for type %s", jsonName, typ.Name)
				}
			}

		case xml.EndElement:
			// done, return the constructed json.OrderedObject
			if ret == obj {

				// insert defaults if present
				for _, e := range typ.JsonDefaults {
					if _, ok := obj.Get(e.JsonName); !ok {
						obj.Set(e.JsonName, e.JsonDefault)
					}
				}

				// modify the json through a custom function if needed
				if nil != typ.JsonHook {
					typ.JsonHook(typ, obj)
				}

				ret = obj.Object
			}
			return

		case xml.CharData:
			// trim borrowed from mxj
			chardata := strings.Trim(string(tokel), "\t\r\b\n ")
			if len(chardata) != 0 {
				if ret == nil {
					if obj, listObj, ret, err = initRetObject(el, typ, true); err != nil {
						return
					}
				}

				converted := convertSimpleToJson(typ, chardata)

				if typ.TextAttr != "" {
					obj.Set(typ.TextAttr, converted)
					ret = obj
				} else {
					ret = converted
				}
			}

		default:
			// ignore anything else
		}
	}
}

func getNextElement(x *xml.Decoder, wantStart bool) (ret interface{}, err error) {
	var tok xml.Token
	for {
		tok, err = x.Token()
		if err != nil {
			return
		}

		switch el := tok.(type) {
		case xml.StartElement:
			if wantStart {
				ret = el
				return
			}
			err = errors.Errorf("unexpected element (wanted end), got %#v", tok)
			return
		case xml.EndElement:
			if !wantStart {
				ret = el
				return
			}
			err = errors.Errorf("unexpected element (wanted start), got %#v", tok)
			return
		default:
			// don't care about various xml elements
		}
	}
}

func getNextStartElement(x *xml.Decoder) (ret xml.StartElement, err error) {
	el, err := getNextElement(x, true)
	if err == nil {
		ret = el.(xml.StartElement)
	}
	return
}

func processSoapElement(d *xml.Decoder, el xml.StartElement, typ *EwsType, jsonType string) (obj json.OrderedObject, err error) {

	// the caller has consumed a start element, expectation is that
	// this function will consume the end element

	// processElement will do that for us, so not needed here

	var anyElem interface{}
	anyElem, err = processElement(d, el, typ)
	if err != nil || anyElem == nil {
		//log.Printf("processSoapElement early return nil")
		return
	}

	// the returned item should be a json.OrderedObject, use that as our SOAP element
	obj, ok := anyElem.(json.OrderedObject)
	if !ok {
		err = errors.Errorf("invalid return value %#v in %s", anyElem, el.Name.Local)
		return
	}

	// set its type hint, and return
	if jsonType != "" {
		prepend := json.OrderedObject{json.Member{Key: "__type", Value: jsonType}}

		// nothing in the slice? weird... but ok, set the type anyways
		if len(obj) == 0 {
			obj = prepend

			// replace the hint if it's already there
		} else if obj[0].Key == "__type" {
			obj[0].Value = jsonType

			// otherwise add a hint
		} else {
			obj = append(prepend, obj...)
		}
	}

	return
}

// SOAP2JSON converts a SOAP message to a json message. This returns a JSON
// message as a buffer of bytes, and the OpDescriptor that can be used to
// decode the returned message via Json2Soap
// .. always client -> server
func SOAP2JSON(r io.Reader) (ret []byte, op *OpDescriptor, err error) {

	var ok bool
	d := xml.NewDecoder(r)

	// consume the envelope
	el, err := getNextStartElement(d)
	if err != nil {
		return
	}

	if el.Name.Local != "Envelope" {
		err = errors.New("not a SOAP document")
		return
	}

	// header is required, but it can be nil
	var header json.OrderedObject
	var body json.OrderedObject
	var msgType string

	gotHeader := false
	gotBody := false

	for !gotHeader || !gotBody {
		el, err = getNextStartElement(d)
		if err != nil {
			return
		}

		switch el.Name.Local {
		case "Header":
			if gotHeader {
				err = errors.New("multiple SOAP headers found")
				return
			}

			header, err = processSoapElement(d, el, EwsSoapRequestHeader, EwsSoapRequestHeader.JsonType)
			if err != nil {
				return
			}

			// HACK: the json header does not follow the normal rules, it appears
			//       that anything with a simple attribute is collapsed
			if header != nil {

				var v json.OrderedObject
				var ver string

				for i, kv := range header {
					switch kv.Key {

					case "RequestServerVersion":

						if v, ok = kv.Value.(json.OrderedObject); ok {
							for _, kv2 := range v {
								if kv2.Key == "Version" {
									if ver, ok = kv2.Value.(string); ok {
										// HACK: The specified server version, Exchange2007_SP1, is not valid for a JSON request.
										//       .. which of course is what mac mail uses, so let's upgrade!
										if strings.HasPrefix(ver, "Exchange2007") || strings.HasPrefix(ver, "Exchange2010") {
											ver = "Exchange2013"
										}

										header[i].Value = ver
									}
								}
							}
						}
					}
				}
			} else {
				// The GLOBAL exchange server is older, and it requires a header,
				// so set that if the requesting client didn't ask for it
				// TODO: version detect for versions of Exchange that care about this
				customHeader := NewOrderedObject()
				customHeader.Set("__type", "JsonRequestHeaders:#Exchange")
				customHeader.Set("RequestServerVersion", "Exchange2013")
				header = customHeader.Object
			}

			gotHeader = true

		case "Body":
			if gotBody {
				err = errors.New("multiple SOAP bodies found")
				return
			}
			// get the next token -- that tells us which operation this is
			el, err = getNextStartElement(d)
			if err != nil {
				return
			}

			op, ok = EwsOperations[el.Name.Local]
			if !ok {
				err = errors.Errorf("Unknown EWS operation %s", el.Name.Local)
				return
			}

			msgType = op.RequestType

			body, err = processSoapElement(d, el, op.Request, op.BodyType)
			if err != nil {
				return
			}

			gotBody = true

			// processSoapElement got rid of the action end tag, still need to
			// remove the body end tag
			_, err = getNextElement(d, false)
			if err != nil {
				return
			}
		}
	}

	// there should be a final EndElement here, followed by an EOF
	_, err = getNextElement(d, false)
	if err != nil {
		return
	}

	// TODO: consume EOF

	// construct the final message and serialize it
	msg := json.OrderedObject{
		{Key: "__type", Value: msgType},
		{Key: "Header", Value: header},
		{Key: "Body", Value: body},
	}

	//ret, err = json.MarshalIndent(msg, "", "  ")
	ret, err = json.Marshal(msg)
	return
}
