package ews

/*
	This converts JSON responses from Exchange to EWS SOAP XML. See
	ews_soap2json for details about the format
*/

import (
	"encoding/xml"
	//"fmt"
	"io"
	//"log"

	"strconv"

	"github.com/pkg/errors"
	"github.com/virtuald/go-ordered-json"
	"github.com/emef/bitfield"
	"strings"
)

//
// JSON -> XML
//

// only use these for deserialization, need ordered type for serialization
type JsonObject map[string]interface{}
type JsonList []interface{}

type JsonSoapMessage struct {
	Type   string `json:"__type"`
	Header map[string]interface{}
	Body   map[string]interface{}
}

// namespaces
const NSSOAP = "http://schemas.xmlsoap.org/soap/envelope/"
const NSMSG = "http://schemas.microsoft.com/exchange/services/2006/messages"
const NSTYPE = "http://schemas.microsoft.com/exchange/services/2006/types"

// xml names/attrs used to construct the resulting XML
var soapEnvelopeTag = xml.Name{Local: "soap:Envelope"}
var soapBodyTag = xml.Name{Local: "soap:Body"}

var soapXmlns = []xml.Attr{
	{Name: xml.Name{Local: "xmlns:soap"}, Value: NSSOAP},
	{Name: xml.Name{Local: "xmlns:m"}, Value: NSMSG},
	{Name: xml.Name{Local: "xmlns:t"}, Value: NSTYPE},
}

// JSON2SOAP converts a json message to a SOAP message
// .. always server -> client
// .. and we always know what type we're expecting
func JSON2SOAP(r io.Reader, op *OpDescriptor, w io.Writer, indent bool) (err error) {

	var msg JsonSoapMessage
	d := json.NewDecoder(r)
	d.UseNumber()

	if err = d.Decode(&msg); err != nil {
		return
	}

	// it appears that golang's XML encoder does not support namespaces in a
	// readable/useful way, so we have to do all the prefixing stuff ourselves

	// add an xml header because yolo
	if _, err = w.Write([]byte(xml.Header)); err != nil {
		return
	}

	// construct the soap stuff
	enc := xml.NewEncoder(w)
	if indent {
		enc.Indent("", " ")
	}

	// begin the envelope
	err = enc.EncodeToken(xml.StartElement{
		Name: soapEnvelopeTag,
		Attr: soapXmlns,
	})
	if err != nil {
		return
	}

	if msg.Header != nil {

		if err = processJson(enc, msg.Header, &EwsSoapResponseHeader); err != nil {
			return errors.Wrap(err, "soap:Header")
		}
	}

	if msg.Body != nil {

		if err = enc.EncodeToken(xml.StartElement{Name: soapBodyTag}); err != nil {
			return
		}

		// given the op, we know what type is being used

		// HACK: All of the responses are basically the same type, while there's a
		// type hint at the level we need it, it's not useful because there are
		// duplicates. So, the solution for this is to set our own type hint

		ewsResponseType := op.Response
		//fmt.Println("***Request:", op.Request.JsonType)
		//fmt.Println("Expected response type (json name)", ewsResponseType.JsonName)

		//ret1, _ := json.Marshal(msg)
		//fmt.Println("original message", string(ret1))

		for childName := range ewsResponseType.SingleType.Type.TypeByElementName {
			childBody := msg.Body[childName]

			if nil == childBody {
				//fmt.Println("Skipping child", childName, "as child has no body")
				continue
			}
			var ok bool
			var childMessage map[string]interface{}

			childMessage, ok = childBody.(map[string]interface{})
			if !ok {
				// this element doesn't need a type hint...
				continue
				//errorMsg := "Internal error: Cannot convert body of '" + childName + "' to map[string]interface{}"
				//return errors.New(errorMsg)
			}
			//fmt.Println("ChildMessage", childMessage)

			//childMessage["__type"] = op.Response.JsonName + "Message"

			var itemsG interface{}
			itemsG, ok = childMessage["Items"]
			if !ok {
				return errors.New("Internal error: Cannot find 'Items' element in '" + childName + "'")
			}

			var items []interface{}
			items, ok = itemsG.([]interface{})
			if !ok {
				return errors.New("Internal error: Cannot convert items to array. Inside element: '" + childName + "'")
			}

			for _, gItem := range items {
				if item, ok := gItem.(map[string]interface{}); ok {
					// add the type hint
					// appending "Message" to the type name because that's what Microsoft does
					item["__type"] = op.Response.JsonName + "Message"
					//fmt.Println("just added type to:", item)
				} else {
					return errors.Errorf("Internal error: item is not a JSON object: %#v", gItem)
				}
			}
		}

		//ret, _ := json.Marshal(msg)
		//fmt.Println("Modified message", string(ret))

		// ok, now we process the element like 'normal'
		if err = processJson(enc, msg.Body, &op.Response); err != nil {
			return errors.Wrap(err, "soap:Body")
		}

		if err = enc.EncodeToken(xml.EndElement{Name: soapBodyTag}); err != nil {
			return
		}
	}

	// envelope
	if err = enc.EncodeToken(xml.EndElement{Name: soapEnvelopeTag}); err != nil {
		return
	}

	return enc.Flush()
}

// element: JSON element to process
// edesc: contains information about the element, always present
func processJson(enc *xml.Encoder, element interface{}, edesc *EwsJsonElement) (err error) {

	// when this is called, the underlying JSON type is uncertain, so we have to
	// inspect it to figure it out

	// note to self: it occurs to me that WCF only emits type hints when a list
	// of things are emitted. Presumably because if an element is encountered, it
	// has to be a particular type. The challenge is when you have a list of things,
	// and the things may be of any type

	/*log.Printf("Processing %v", edesc)
	if edesc.Type != nil {
		log.Printf("-> type %s", edesc.Type.Name)
	}
	if lookupType != nil {
		log.Printf("-> lookup type %s", lookupType.Name)
	}*/

	switch el := element.(type) {
	case map[string]interface{}:

		if err = processJsonObject(enc, el, edesc); err != nil {
			return errors.Wrap(err, edesc.JsonName)
		}

	case []interface{}:

		if err = processJsonList(enc, el, edesc); err != nil {
			return errors.Wrap(err, edesc.JsonName)
		}

	case nil:

		/*if edesc.SingleType != nil {
			if err = edesc.SingleType.EmitStart(enc, nil); err != nil {
				return errors.Wrap(err, edesc.JsonName)
			}

			if err = edesc.SingleType.EmitEnd(enc); err != nil {
				return errors.Wrap(err, edesc.JsonName)
			}
		}*/

	default:
		// this is a simple type, convert it to a string and emit it
		if !edesc.IsCharData() {
			return errors.Errorf("%s: unexpected simple content `%#v`", edesc.JsonName, el)
		}

		if err = edesc.SingleType.EmitStart(enc, nil); err != nil {
			return errors.Wrap(err, edesc.JsonName)
		}

		var text string
		if text, err = toString(el); err != nil {
			return errors.Wrap(err, edesc.JsonName)
		}

		ewsType := edesc.SingleType.Type
		if ewsType.IsSimple && ewsType.SimpleType == T_ENUM {
			// find chardata in enum_values
			num, ierr := strconv.Atoi(text)
			if nil != ierr {
				return errors.Wrap(ierr, "Unable to convert " + text + " to an integer")
			}
			text = ewsType.EnumValues[num]
		}

		if err = processJsonChardata(enc, text); err != nil {
			return errors.Wrap(err, edesc.JsonName)
		}

		if err = edesc.SingleType.EmitEnd(enc); err != nil {
			return errors.Wrap(err, edesc.JsonName)
		}
	}

	return
}

// element: json element to process
// edesc: describes the element that is being processed, non-nil
// lookupType: the parent type that the element resides in (may be nil)
func processJsonObject(enc *xml.Encoder, element map[string]interface{}, edesc *EwsJsonElement) (err error) {

	//ret1, _ := json.Marshal(element)
	//fmt.Println("processJsonObject", "elemnt:", string(ret1))

	// if there's only a single type associated with this element, then this
	// is super easy -- just use it
	jtyp := edesc.SingleType

	// however, if there are multiple types, then we need to decide which type it is
	if jtyp == nil {
		
		// we use a hook helper if it exists
		if edesc.XmlChoiceHook != nil {
			jtyp, err = edesc.XmlChoiceHook(edesc, element)
			if err != nil {
				return
			}
			
		} else {
			// otherwise use the type hint
			hint, ok := element["__type"].(string)
			if !ok {
				return errors.Errorf("no hint, cannot determine type for %+v", element)
			}

			jtyp = edesc.Types[hint]
			
			if jtyp == nil {
				return errors.Errorf("hint %s was not found in element %s", hint, edesc.JsonName)
			}
		}
	}

	typ := jtyp.Type

	// delete the type hint if present
	delete(element, "__type")

	// make sure we're sane
	if typ.IsSimple && typ.TextAttr == "" {
		return errors.Errorf("%s is a simple type", typ.Name)
	}

	// process any potential attrs first
	var attrs []xml.Attr

	// iterate over Attributes to keep the output deterministic
	for _, attr := range typ.Attributes {
		aname := attr.JN
		if o, ok := element[aname]; ok {
			var attrStr string
			if attrStr, err = toString(o); err != nil {
				return errors.Wrapf(err, "invalid attribute %s", aname)
			}

			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: attr.XN}, Value: attrStr})
			delete(element, aname)
		}
	}

	if err = jtyp.EmitStart(enc, attrs); err != nil {
		return
	}

	// is it a simple type with a text attr? if so, then emit chardata
	if typ.IsSimple && typ.TextAttr != "" {
		
		if o, ok := element[typ.TextAttr]; ok {
			if err = processJsonChardata(enc, o); err != nil {
				err = errors.Wrap(err, typ.TextAttr)
				return
			}

			delete(element, typ.TextAttr)
		}
		// else if it's not present.. then not an error, just no text to emit

		// special case for certain types of lists
	} else if typ.JsonListName != "" {
		
		if element[typ.JsonListName] == nil {
			return errors.Errorf("No %s element found for element with items?", typ.JsonListName)
		}

		// previously:
		// (typ.IsList && len(element) == 1 && element["Items"] != nil)

		if err = processJson(enc, element[typ.JsonListName], typ.JsonListElement); err != nil {
			return
		}

		delete(element, typ.JsonListName)

	} else {		
		// the output XML must be done in the correct order. Read from the
		// JsonElementList and pop it from the JSON map sequentially

		// recurse to the next level of elements
		for _, je := range typ.JsonElementList {
			if obj, ok := element[je.JsonName]; ok {

				if nil != je.SingleType && je.SingleType.Type.IsSimple && je.SingleType.Type.SimpleType == T_LIST && nil != je.SingleType.Type.ListItemType && je.SingleType.Type.ListItemType.IsSimple && je.SingleType.Type.ListItemType.SimpleType == T_ENUM {
					jeTyp := je.SingleType.Type

					// process as a list of simple elements
					var numStr string
					var jeerr error
					if numStr, jeerr = toString(obj); jeerr != nil {
						return errors.Wrap(jeerr, "Unable to convert list value to string")
					}
					
					num, atoierr := strconv.Atoi(numStr)
					if nil != atoierr {
						return errors.Wrap(atoierr, "Unable to convert " + numStr + " to integer")
					}
					
					bits := bitfield.NewFromUint32(uint32(num))
					var names []string
					for index, value := range jeTyp.ListItemType.EnumValues {
						if bits.Test(uint32(index)) {
							names = append(names, value)
						}
					}
					
					text := strings.Join(names, " ")

					if err = je.SingleType.EmitStart(enc, nil); err != nil {
						return errors.Wrap(err, je.JsonName)
					}

					enc.EncodeToken(xml.CharData([]byte(text)))
					
					if err = je.SingleType.EmitEnd(enc); err != nil {
						return errors.Wrap(err, je.JsonName)
					}

				} else {
					if err = processJson(enc, obj, je); err != nil {
						return
					}
				}
				delete(element, je.JsonName)
			}
		}
	}

	// finally, remove any extra elements that are present in JSON but
	// aren't supposed to be in the XML
	for _, extra := range typ.JsonExtra {
		delete(element, extra)
	}

	if len(element) != 0 {
		// TODO: don't be so strict
		return errors.Errorf("extra elements in %s: %#v", typ.Name, element)
	}

	// end element and we're done
	return jtyp.EmitEnd(enc)
}

// elements: json content
// edesc: describes the element we're decoding
// lookupType: type that the element is present in
func processJsonList(enc *xml.Encoder, elements []interface{}, edesc *EwsJsonElement) (err error) {

	//start DEBUG
	/*
	  log.Printf("-- Top processJsonList", edesc.JsonName, edesc.SingleType)
	  if edesc.SingleType != nil {
	    log.Printf("name: ", edesc.SingleType.Type.Name)
	    log.Printf("JsonType: ", edesc.SingleType.Type.JsonType)
	    log.Printf("IsList: ", edesc.SingleType.Type.IsList)
	    log.Printf("IsSimple: ", edesc.SingleType.Type.IsSimple)
	    log.Printf("IsCollapsed: ", edesc.SingleType.Type.IsCollapsed)
	    log.Printf("JsonListName: ", edesc.SingleType.Type.JsonListName)
	  }
	  log.Printf("----")
	*/
	//end DEBUG

	// used to deserialize the children
	// - needs to contain whatever needed to lookup types if necessary
	childDesc := &EwsJsonElement{
		JsonName: edesc.JsonName,
	}

	//log.Printf("edesc %#v", edesc)

	// tag comes from edesc
	var jtyp *EwsJsonType

	// favor the closest element if it's a single type
	// .. if it's a choice, we're screwed
	if edesc.SingleType != nil &&
		(edesc.SingleType.Type.JsonListName != "" || edesc.SingleType.Type.IsList) {

		jl := edesc.SingleType.Type.JsonListElement
		childDesc.SingleType = jl.SingleType
		childDesc.Types = jl.Types
		jtyp = edesc.SingleType

	} else if edesc.IsList {
		childDesc = edesc

		// not relevant?
		//} else if lookupType != nil && (lookupType.JsonListName != "" || lookupType.IsList) {
		//	listType = lookupType

	} else {
		return errors.New("Could not determine list type")
	}

	//emitTag := false
	//if edesc.XmlTag.Local != "" && edesc.XmlTag.Local != childDesc.XmlTag.Local {
	//	emitTag = true
	//}

	if jtyp != nil {
		//log.Printf("Emitting start", jtyp)
		if err = jtyp.EmitStart(enc, nil); err != nil {
			return
		}
	}

	// for each item in the list
	for _, e := range elements {
		// sometimes exchange does this
		if e == nil {
			continue
		}

		if childDesc.IsCharData() {

			// process each item as chardata

			if err = childDesc.SingleType.EmitStart(enc, nil); err != nil {
				return
			}

			if err = processJsonChardata(enc, e); err != nil {
				return errors.Wrap(err, "processing list")
			}

			if err = childDesc.SingleType.EmitEnd(enc); err != nil {
				return
			}

		} else {
			// process each item as an object
			obj, ok := e.(map[string]interface{})
			if !ok {
				return errors.Errorf("while processing list, expected object, got %#v", e)
			}

			if err = processJsonObject(enc, obj, childDesc); err != nil {
				return
			}
		}

	}

	// end element and we're done
	if jtyp != nil {
		//log.Printf("Emitting end", jtyp)
		if err = jtyp.EmitEnd(enc); err != nil {
			return
		}
	}

	return
}

// emits an xml.CharData instruction
func processJsonChardata(enc *xml.Encoder, el interface{}) (err error) {
	var text string
	if text, err = toString(el); err != nil {
		return
	}

	return enc.EncodeToken(xml.CharData([]byte(text)))
}

// toString converts JSON leafs to a string
func toString(o interface{}) (string, error) {
	switch oo := o.(type) {
	case bool:
		if oo {
			return "true", nil
		}
		return "false", nil
	case json.Number:
		return string(oo), nil
	case string:
		return oo, nil
	case nil:
		// TODO?
		return "", nil
	default:
		return "", errors.Errorf("expected simple type, got `%#v`", oo)
	}
}
