#!/usr/bin/env python
'''
    This tool generates golang structures that are used at runtime to help with
    converting the EWS SOAP XML API to/from the OWA JSON API.

    Maybe in the future we autogenerate the converter. Not now.
'''

# Note: MS's APIs deal with arrays in particular ways, to learn more
# see https://msdn.microsoft.com/en-us/library/d3hx2s7e(v=vs.100).aspx

from __future__ import print_function

from collections import OrderedDict
import copy
from os.path import abspath, dirname, join
import sys
import xml.etree.ElementTree as ET

import xmlschema
from xmlschema.builtins import XSD_BUILTIN_TYPES
    
from xmlschema.components.xsdbase import get_xsd_attribute, get_xsd_component
from xmlschema.components.elements import XsdComplexType, XsdGroup
from xmlschema.qnames import split_qname, split_reference, XSD_CHOICE_TAG

ns_x = 'http://www.w3.org/2001/XMLSchema'

namespaces = {
    'http://schemas.microsoft.com/exchange/services/2006/messages': 'm',
    'http://schemas.microsoft.com/exchange/services/2006/types': 't',
    ns_x: 'xs'
}

def shorten_qname(n):
    ns, n = split_qname(n)
    return '%s:%s' % (namespaces[ns], n)


# https://github.com/brunato/xmlschema/issues/10
def _extends_type(self):
    '''Hack on an 'extends_type' property to XsdComplexType'''
    if not self.has_extension():
        return None
    content_spec = get_xsd_component(self.content_type.elem)
    content_base = get_xsd_attribute(content_spec, 'base', default=None)
    if content_base is None:
        return
    base_qname, namespace = split_reference(content_base, self.schema.namespaces)
    return self.schema.maps.lookup_type(base_qname)

XsdComplexType.extends_type = property(fget=_extends_type)



class ElementData(object):

    __slots__ = ['type', 'is_list', 'json_name', 'json_hint', 'xml_name', 'json_default']

    def __init__(self, typ):

        self.type = typ

        # -> this is_list is when the element is marked as a list, which is
        #    different from the type being marked as a list
        self.is_list = False

        # Only set manually when the JSON name differs from the XML name
        self.json_name = None
        
        # Only set manually when the JSON hint cannot be deduced
        self.json_hint = None

        # HACK: Only set when the element name is not actually correct
        self.xml_name = None

        # Set manually when the XML and JSON disagree about defaults
        # -> if the XML does not contain a value for this element, insert this
        self.json_default = None

class TypeData(object):
    '''
        Holds all of the data needed for transformation for XML schemas
    '''

    __slots__ = [
        'namespace', 'name', 'elements', 'attrs', 'json_extra',
        'any_attr', 'simple_type', 'json_text_attr', 'elem',
        'is_abstract', 'is_list', 'json_list_name', 'json_name',
        'enum_values', 'list_item_type'
    ]

    def __init__(self, namespace, name, elem, is_abstract):

        self.namespace = namespace
        self.name = name
        self.is_abstract = is_abstract

        # key: qname
        # value: ElementData
        # -> keep the elements in insertion order, so we can emit the
        #    xml elements in the correct order
        self.elements = OrderedDict()
        self.attrs = {}

        # extra attributes that are returned from JSON that the XML doesn't support
        # -> set manually
        self.json_extra = []

        self.any_attr = False

        self.simple_type = None

        # only needed if there are attrs and this is a simple type
        self.json_text_attr = None

        # XML element representing this type for debugging
        self.elem = elem

        # do we think this is a list?
        # -> this is for when a sequence or choice element is marked
        #    with maxOccurs=unbounded; if an element is marked directly,
        #    then that is treated differently
        self.is_list = False

        # only set manually at this time
        self.json_list_name = None

        # only set manually at this time
        self.json_name = None

        # set later
        self.enum_values = []
        self.list_item_type = None

    @property
    def qname(self):
        return '{%s}%s' % (self.namespace, self.name)

    def finish(self):
        # If it's a simple type, and there are attrs, then we need to look it
        # up in a map, as we have no idea what the type actually is
        if self.attrs and self.simple_type:
            # assume that it's Value, as that's what we've seen so far...
            self.json_text_attr = 'Value'

    def __repr__(self):
        #dump(self.elem)
        return '<TypeData %s elems=%r attrs=%r%s>' % (
            self.name, self.elements, self.attrs,
            '' if not self.simple_type else ' simple'
        )


class SoapOperation(object):
    '''
        Holds data needed to do SOAP operation transformation
    '''

    def __init__(self, action, in_elem, out_elem,
                               in_headers, out_headers):
        self.action = action

        # these are names of the elements in the xsd
        self.in_elem = in_elem
        self.out_elem = out_elem

        # as are these
        self.in_headers = in_headers
        self.out_headers = out_headers


def _create_choice_hacks():

    m = '{http://schemas.microsoft.com/exchange/services/2006/messages}'
    t = '{http://schemas.microsoft.com/exchange/services/2006/types}'

    # TODO: I think the arrays might be incorrect..

    return {
        m + "ArrayOfResponseMessagesType": "Items",
        m + "ArrayOfServiceConfigurationType": "",
        m + "FindConversationType:0": "Paging",
        m + "FindFolderType:0": "Paging",
        m + "FindItemType:0": "Paging",
        m + "FindItemType:1": "Grouping",
        m + "GetAppManifestsResponseType:0": "",
        m + "GetLastPrivateCatalogUpdateResponseType:0": "",
        m + "SubscribeType:0": "SubscriptionRequest",

        t + "AggregateOnType": "AggregationProperty",
        t + "AppendToFolderFieldType:0": "Folder",
        t + "AppendToItemFieldType:0": "Item",
        t + "ArrayOfArraysOfTrackingPropertiesType": "",
        t + "ArrayOfAttachmentsType": "",
        t + "ArrayOfAttendeeConflictData": "",
        t + "ArrayOfBaseItemIdsType": "ItemId",
        t + "ArrayOfCalendarItemsType": None,
        t + "ArrayOfCalendarPermissionsType": None,
        t + "ArrayOfConversationNodesType": None,
        t + "ArrayOfConversationRequestsType": None,
        t + "ArrayOfConversationsType": None,
        t + "ArrayOfDistinguishedFolderIdType": None,
        t + "ArrayOfEncryptedSharedFolderDataType": None,
        t + "ArrayOfEventIDType": None,
        t + "ArrayOfFindMessageTrackingSearchResultType": None,
        t + "ArrayOfFolderIdType": None,
        t + "ArrayOfFoldersType": "Folder",
        t + "ArrayOfGroupIdType": None,
        t + "ArrayOfGroupedItemsType": None,
        t + "ArrayOfImGroupType": None,
        t + "ArrayOfInvalidRecipientsType": None,
        t + "ArrayOfItemClassType": None,
        t + "ArrayOfItemsType": None,
        t + "ArrayOfPeopleType": None,
        t + "ArrayOfPermissionsType": None,
        t + "ArrayOfPersonType": None,
        t + "ArrayOfRecipientTrackingEventType": None,
        t + "ArrayOfRecipientsType": None,
        t + "ArrayOfRuleOperationsType": None, #? array?
        t + "ArrayOfSmtpAddressType": None,
        t + "ArrayOfTrackingPropertiesType": None,
        t + "ArrayOfUnknownEntriesType": None,
        t + "BaseObjectChangedEventType:0": None,
        t + "ConfigurationRequestDetailsType": None,
        t + "ConnectingSIDType": None,
        t + "ExtendedPropertyType:0": None,
        t + "FieldURIOrConstantType": "Item",
        t + "FindItemParentType": None,
        t + "FolderChangeType:0": "FolderId",
        t + "GroupByType:0": "GroupByProperty",
        t + "ItemAttachmentType:0": "Item",
        t + "ItemChangeType:0": "ItemId",
        t + "ModifiedEventType:0": None,
        t + "MovedCopiedEventType:0": None,
        t + "MovedCopiedEventType:1": None,
        t + "NonEmptyArrayOfAlternateIdsType": None,
        t + "NonEmptyArrayOfAttachmentsType": None,
        t + "NonEmptyArrayOfBaseFolderIdsType": None,
        t + "NonEmptyArrayOfBaseItemIdsType": None,
        t + "NonEmptyArrayOfExtendedFieldURIs": None,
        t + "NonEmptyArrayOfExtendedFieldURIsType": None,
        t + "NonEmptyArrayOfExtendedPropertyType": None,
        t + "NonEmptyArrayOfFolderChangeDescriptionsType": None,
        t + "NonEmptyArrayOfFoldersType": None,
        t + "NonEmptyArrayOfItemChangeDescriptionsType": None,
        t + "NonEmptyArrayOfNotificationEventTypesType": None,
        t + "NonEmptyArrayOfPathsToElementType": None,
        t + "NonEmptyArrayOfPropertyValuesType": None,
        t + "NonEmptyArrayOfRequestAttachmentIdsType": None,
        t + "NonEmptyArrayOfResponseObjectsType": None,
        t + "NonEmptyStateDefinitionType": None,
        t + "NotificationType:0": "Events",
        t + "ProtectionRuleConditionType": "",
        t + "SearchFolderScopeType": "BaseFolderId",
        t + "SetFolderFieldType:0": "Folder",
        t + "SetItemFieldType:0": "Item",
        t + "SingleRecipientType": None,
        t + "SyncFolderHierarchyCreateOrUpdateType": "Folder",
        t + "SyncFolderItemsCreateOrUpdateType": "Item",
        t + "TargetFolderIdType": "BaseFolderId",
        t + "UserConfigurationNameType": "BaseFolderId",
    }

choice_hacks = _create_choice_hacks()

def apply_hacks(operations, types, elements):
    '''
        This is used to manually adjust special cases that just don't fit
    '''

    m = '{http://schemas.microsoft.com/exchange/services/2006/messages}'
    t = '{http://schemas.microsoft.com/exchange/services/2006/types}'

    #
    # Messages namespace
    #

    types[m + "ArrayOfResponseMessagesType"].json_list_name = "Items"

    types[m + "CreateAttachmentResponseType"].json_extra = [
        'SharingInformation'
    ]

    e = types[m + "FindFolderType"].elements
    e[m + 'IndexedPageFolderView'].json_default = 'json.OrderedObject{json.Member{"__type", "IndexedPageView:#Exchange"}, json.Member{"MaxEntriesReturned", 2147483647}, json.Member{"Offset", 0}, json.Member{"BasePoint", "Beginning"}}'

    types[m + "FindItemResponseMessageType"].json_extra = [
        'IsSearchInProgress','SearchFolderId'
    ]

    types[m + "SyncFolderItemsResponseMessageType"].json_extra = [
        'OldestReceivedTime', 'MoreItemsOnServer', 'TotalCount'
    ]
    
    #
    # Types namespace
    #

    types[t + "AbchEmailAddressDictionaryType"].json_name = 'AbchEmailAddressDictionaryType'

    types[t + 'AbchPersonItemType'].json_name = 'AbchPerson'
    types[t + 'AddressEntityType'].json_name = 'AddressEntityType'
    types[t + 'AggregateOnType'].json_name = 'AggregateOnType'
    types[t + 'ApprovalRequestDataType'].json_name = 'ApprovalRequestDataType'


    e = types[t + 'ArrayOfResolutionType'].elements
    e[t + 'Resolution'].json_name = 'Resolutions'

    types[t + 'BodyType'].json_name = 'BodyContentType'

    e = types[t + "CalendarItemType"].elements
    e[t + 'LegacyFreeBusyStatus'].json_name = 'FreeBusyType'
    e[t + 'MyResponseType'].json_name = 'ResponseType'

    types[t + 'CalendarItemType'].json_extra = [
    'Charm'
    ]

    types[t + 'CalendarFolderType'].json_extra = [
    'Charm'
    ]

    types[t + 'ConstantValueType'].json_name = 'Constant'
    types[t + 'ContactItemType'].json_name = 'Contact'

    types[t + "ContactUrlDictionaryType"].json_name = 'ContactUrlDictionaryType'
    types[t + "ContainsExpressionType"].json_name = 'Contains'

    types[t + "DayOfWeekType"].simple_type = "enum"
    
    # TODO we should automatically handle when processing the schema
    types[t + "DaysOfWeekType"].simple_type = "list"
    types[t + "DaysOfWeekType"].list_item_type = 'DayOfWeekType'

    types[t + "EmailAddressType"].json_extra = [
        'EmailAddressIndex', 'RelevanceScore', 'SipUri', 'Submitted',
    ]

    types[t + "EmailAddressDictionaryEntryType"].json_name = 'EmailAddressDictionaryEntryType'
    types[t + "EmailAddressDictionaryEntryType"].json_text_attr = 'EmailAddress'

    types[t + "ExtendedPropertyType"].json_name = 'ExtendedPropertyType'

    types[t + "FieldOrderType"].json_name = 'SortResults'

    types[t + 'FieldURIOrConstantType'].json_name = 'FieldURIOrConstantType'

    types[t + "ImAddressDictionaryEntryType"].json_name = 'ImAddressDictionaryEntryType'
    types[t + "ImAddressDictionaryEntryType"].json_text_attr = 'ImAddress'

    types[t + 'MeetingCancellationMessageType'].json_name = 'MeetingCancellationMessageType'
    types[t + 'MeetingRequestMessageType'].json_name = 'MeetingRequestMessageType'
    types[t + 'MeetingResponseMessageType'].json_name = 'MeetingResponseMessageType'

    types[t + 'MessageSafetyType'].json_name = 'MessageSafetyType'

    types[t + "MessageType"].json_extra = [
        'Apps', 'IsGroupEscalationMessage',
        'MessageResponseType', 'ParentMessageId',
        'ReceivedOrRenewTime', 'RecipientCounts',
    ]

    types[t + 'MimeContentType'].json_name = 'MimeContentType'
    types[t + 'NetworkItemType'].json_name = 'NetworkItemType'

    # OWA json indicates these names are different in JSON/XML
    types[t + "PathToExtendedFieldType"].json_name = "ExtendedPropertyUri"
    types[t + "PathToIndexedFieldType"].json_name = "DictionaryPropertyUri"
    types[t + "PathToUnindexedFieldType"].json_name = "PropertyUri"

    types[t + "PhoneNumberDictionaryEntryType"].json_name = 'PhoneNumberDictionaryEntryType'
    types[t + "PhoneNumberDictionaryEntryType"].json_text_attr = 'PhoneNumber'

    types[t + "PhysicalAddressDictionaryType"].json_name = 'PhysicalAddressDictionaryType'

    types[t + "ReminderMessageDataType"].json_name = "ReminderMessageDataType"
    types[t + "RequestAttachmentIdType"].json_name = "AttachmentId"

    # really should work here, but we don't have defaults for attributes, so using the jsonHook mechanism in ews_types.go
    #e = types[m + "ResolveNamesType"].attrs
    #e['ContactDataShape'].json_default = 'Default'

    types[t + 'RestrictionType'].json_name = 'RestrictionType'
    re = types[t + 'RestrictionType'].elements

    types[t + 'RoleMemberItemType'].json_name = 'RoleMember'

    types[t + 'SingleRecipientType'].json_name = 'SingleRecipientType'

    # search expressions are weird
    other = ['Contains']
    two = ['And', 'Or', 'Near', 'Not']
    one = ['Exists',
           'IsEqualTo', 'IsGreaterThan', 'IsGreaterThanOrEqualTo',
           'IsLessThan', 'IsLessThanOrEqualTo', 'IsNotEqualTo']

    all_searchexp = other + two + one

    for tt in all_searchexp:
        re[t + tt].json_name = 'Item'

    for tt in two:
        et = types[t + tt + 'Type'].elements
        for en in all_searchexp:
            et[t + en].json_name = 'Item'

    for tt in one + ['ContainsExpression']:
        et = types[t + tt + 'Type'].elements
        et[t + 'ExtendedFieldURI'].json_name = 'Item'
        et[t + 'FieldURI'].json_name = 'Item'
        et[t + 'IndexedFieldURI'].json_name = 'Item'

    # end search expression madeness
    
    types[t + "SyncFolderItemsChangesType"].json_list_name = "Changes"
    e = types[t + "SyncFolderItemsChangesType"].elements
    e[t + 'Create'].json_hint = 'SyncFolderItemsCreateType:#Exchange'
    e[t + 'Update'].json_hint = 'SyncFolderItemsUpdateType:#Exchange'
    
    types[t + "SyncFolderHierarchyChangesType"].json_list_name = "Changes"
    types[t + "SyncFolderHierarchyCreateOrUpdateType"].json_extra = [
        'ChangeType',
    ]

    types[t + "SyncFolderItemsCreateOrUpdateType"].json_extra = [
        'ChangeType'
    ]

    types[t + "SyncFolderItemsDeleteType"].json_extra = [
        'ChangeType'
    ]
    types[t + "SyncFolderItemsReadFlagType"].json_extra = [
        'ChangeType'
    ]

    types[t + "TimeZoneDefinitionType"].json_name = 'TimeZoneDefinitionType'
    types[t + 'UserConfigurationNameType'].json_name = 'UserConfigurationNameType'
    types[t + "VotingInformationType"].json_name = 'VotingInformationType'

    # print(operations)
    # GetUserAvailability is also called GetUserAvailabilityRequest
    operations['GetUserAvailabilityRequest'] = operations['GetUserAvailability']


def process_element(e, elements, types, cls_hierarchy):
    typ = getattr(e, 'type', None)
    if not typ:
        return None, None

    ename = e.name
    data = process_type(typ, types, ename, cls_hierarchy)
    if ename in elements:
        #raise Exception("Internal error: duplicate element name %s" % ename)
        # TODO: this occurs because of bugs in xmlschema, and upgrading is a pain
        print("Warning: duplicate element name", ename)
        return None, None

    if data:
        edata = ElementData(data)
    else:
        edata = ElementData(None)

    if e.max_occurs == None or e.max_occurs > 1:
        edata.is_list = True

    elements[ename] = edata
    return ename, edata

def process_type(typ, types, parent_name, cls_hierarchy):

    typ_qname = typ.name

    data = types.get(typ_qname)
    if data:
        return data

    # create named types
    elif typ_qname:
        # split namespace/name
        typ_ns, typ_name = split_qname(typ_qname)
        if typ_ns in namespaces:
            data = TypeData(typ_ns, typ_name, typ.elem, getattr(typ, 'abstract', False))
            types[typ.name] = data
    # and unnamed types if the anonymous type isn't simple
    elif not typ.is_simple() and parent_name:
        _, parent_name = split_qname(parent_name)
        typ_name = parent_name + 'AnonType'
        data = TypeData(None, typ_name, typ.elem, getattr(typ, 'abstract', False))
        types[typ_name] = data

    # process attributes first
    for attrname, attr in getattr(typ, 'attributes', {}).items():
        # the 'any' attribute is represented as None
        if not attrname:
            if data:
                data.any_attr = True
            continue

        if not data:
            # it seems like the only things that fall into this category
            # are things in the SOAP Header -- will need to deal with them
            # in a custom way?
            continue

        _, attr_name = split_qname(attrname)
        data.attrs[attr_name] = process_type(attr.type, types, None, cls_hierarchy)

    if typ.is_simple():
        # default to string
        simple_type = 'string'

        if hasattr(typ, 'primitive_type'):
            _, pname = split_qname(typ.primitive_type.name)
            if pname in ['boolean', 'decimal']:
                simple_type = pname

        if data:
            data.simple_type = simple_type
        else:
            # always return some type
            data = types['{%s}%s' % (ns_x, simple_type)]

        # check for enumeration
        if hasattr(typ, 'elem'):
            restriction = typ.elem
            data.enum_values = []
            for child in restriction.getchildren():
                    ns, tagname = split_qname(child.tag)
                    if tagname == 'enumeration':
                        value = child.attrib['value']
                        data.enum_values.append(value)


    else:
        # two ways to determine if I'm a list (which is different than the
        # contained elements being a list)
        # -> xsd:choice: if my content_type has max_occurs == None
        # -> xsd:sequence: if I have a single child, and it's max_occurs == None

        content = typ.content_type
        content_is_choice = False

        if isinstance(content, XsdGroup) and content.model == XSD_CHOICE_TAG:
            content_is_choice = True

        # if this is a group, and the content is a single anonymous group,
        # then just fold it together
        if isinstance(content, XsdGroup) and len(content) == 1 and \
           content[0].name is None and isinstance(content[0], XsdGroup):
           content = content[0]

        content_elements = [el for el in content]

        # bug in xmlschema -- it adds base elements to a content type group
        # if it exists -- so switch it up
        if isinstance(content, XsdGroup) and \
            typ.extends_type and \
            content.model == XSD_CHOICE_TAG:

            new_elements = []

            # subtract the base types from the group
            base = typ.extends_type

            for e in base.content_type:
                new_elements.append(content_elements.pop(content_elements.index(e)))

            # if there are no elements left, then the choice is actually in the base
            # -> but if there are, then add a pseudo group
            if not content_elements:
                content_elements = new_elements
            else:
                new_elements.append(XsdGroup(model=XSD_CHOICE_TAG, initlist=content_elements))
                content_elements = new_elements
                content_is_choice = False

        if content.max_occurs is None or content.max_occurs > 1:
            data.is_list = True

        if data:
            elements = data.elements
        else:
            elements = {}

        def _process(el, json_name):
            ename, edata = process_element(el, elements, types, cls_hierarchy)
            if edata and json_name:
                edata.json_name = json_name

            # If the element is part of a substitution group, then we have to
            # insert all of its potential substitutes into the set of elements

            # BUT if it does not reside in a choice element then we must set
            # json name to the base element name
            # -> I don't remember why I thought that, but it doesn't seem to be true

            subst = typ.schema.maps.substitution_groups.get(el.name)
            if subst:

                # if the subst element has max_occurs, then the parent is
                # actually a list, not the elements (because this is essentially
                # the same as a choice element)
                if el.max_occurs is None or el.max_occurs > 1:
                    data.json_list_name = 'Items'

                for sel in subst:
                    _, edata = process_element(sel, elements, types, cls_hierarchy)
                    # TODO: necessary?
                    #if p.model != XSD_CHOICE_TAG:
                    edata.json_name = split_qname(el.name)[1]

            # turns out that the same thing applies to abstract types
            elif edata and edata.type.is_abstract and not edata.type.simple_type:
                children = cls_hierarchy[edata.type.qname]
                for i, child in enumerate(children):
                    ctype = process_type(child, types, None, cls_hierarchy)
                    cdata = copy.deepcopy(edata)
                    cdata.type = ctype
                    cdata.xml_name = ename
                    if not cdata.json_name:
                        cdata.json_name = split_qname(ename)[1]
                    if child.name in elements:
                        raise ValueError("Internal error, need to figure this name thing out")
                    elements[child.name] = cdata


        # So this is difficult -- any time we encounter a choice element,
        # JSON has a tendency to squish them together, so we need to set them
        # all to the same json name. However...

        # there isn't a way to predict the name in all cases, so instead
        # we have a lookup table that ensures we cover all of the bases. Oi.
        def _get_group_json_name(tname, idx):
            if idx is not None:
                name = '%s:%s' % (tname, idx)
            else:
                name = tname

            return choice_hacks[name]

        outer_json_name = None
        inner_json_name = None
        json_idx = 0
        # recurse to any underlying elements
        for el in content_elements:
            if isinstance(el, XsdGroup):
                if outer_json_name is not None:
                    raise ValueError()
                if el.model == XSD_CHOICE_TAG:
                    inner_json_name = _get_group_json_name(typ_qname, json_idx)
                    if inner_json_name:
                        json_idx += 1

                for iel in el:
                    # TODO: too many special cases.. this needs cleanup
                    if getattr(iel, 'type', None) is None:
                        for eel in iel:
                            _process(eel, inner_json_name)
                    else:
                        _process(iel, inner_json_name)
            else:
                if content_is_choice:
                    if inner_json_name is not None:
                        raise ValueError()
                    elif outer_json_name is None:
                        outer_json_name = _get_group_json_name(typ_qname, None)

                _process(el, outer_json_name)

        # if there is a single element and that element is a list, then
        # we collapse the element to its parent
        if len(data.attrs) == 0 and len(elements) == 1 and elements.values()[0].is_list:
            data.is_list = True

    return data


def process_builtins(types, cls_hierarchy):

    # ensure these exist so we can process anonymous simple types
    for t in ['string', 'boolean', 'decimal']:
        typ = XSD_BUILTIN_TYPES['{%s}%s' % (ns_x, t)]
        process_type(typ, types, None, cls_hierarchy)


def process_schema(fname, elements, types, cls_hierarchy):
    xsd = xmlschema.XMLSchema(fname)

    # process the class hierarchy first
    for typ in xsd.types.values():
        tex = getattr(typ, 'extends_type', None)
        while tex is not None:
            if tex.name:
                cls_hierarchy.setdefault(tex.name, set()).add(typ)
            tex = getattr(tex, 'extends_type', None)

    # assumption is that the client/server are creating correct things,
    # so we're not going to try and correct them... and if they do create
    # incorrect things, we're just the middleman anyways, so no harm

    for el in xsd.elements.values():
        process_element(el, elements, types, cls_hierarchy)

    for typ in xsd.types.values():
        process_type(typ, types, None, cls_hierarchy)



def process_wsdl(fname, operations):

    ns = {
        'soap': 'http://schemas.xmlsoap.org/wsdl/soap/',
        'wsdl': 'http://schemas.xmlsoap.org/wsdl/',

        # TODO: should extract from WSDL, but lazy...
        'tns': 'http://schemas.microsoft.com/exchange/services/2006/messages',
        't': 'http://schemas.microsoft.com/exchange/services/2006/types',
    }

    def _expandns(n):
        if ':' in n:
            s = n.split(':')
            n = '{%s}%s' % (ns[s[0]], s[1])
        return n

    messages = {}

    tree = ET.parse(fname)
    root = tree.getroot()

    # gather the messages
    for child in root.findall('wsdl:message', ns):
        # map part name to element
        msg = {}

        for part in child.findall('wsdl:part', ns):
            msg[part.attrib['name']] = _expandns(part.attrib['element'])

        messages[child.attrib['name']] = msg

    # iterate the port type to bind them
    # .. who came up with this crap
    port_ops = {}
    port_type = root.find('wsdl:portType', ns)
    for child in port_type.findall('wsdl:operation', ns):

        in_msg = messages[child.find('wsdl:input', ns).attrib['message'].split(':')[1]]
        out_msg = messages[child.find('wsdl:output', ns).attrib['message'].split(':')[1]]

        port_ops[child.attrib['name']] = {
            'in': in_msg,
            'out': out_msg,
        }

    # for each operation, extract the message types
    binding = root.find('wsdl:binding', ns)
    for child in binding.findall('wsdl:operation', ns):
        op_name = child.attrib['name']

        # get the input and check
        op_input = child.find('wsdl:input', ns)
        body = list(op_input.findall('soap:body', ns))

        # make sure it matches what it should be
        if len(body) != 1 or body[0].attrib['use'] != 'literal':
            raise ValueError("Unexpected body: %s/%s" % (op_name, body))

        body = body[0]
        in_elem = port_ops[op_name]['in'][body.attrib['parts']]

        # get the output
        op_output = child.find('wsdl:output', ns)
        body = list(op_output.findall('soap:body', ns))

        # make sure it matches what it should be
        if len(body) != 1 or body[0].attrib['use'] != 'literal':
            raise ValueError("Unexpected body: %s" % body)

        body = body[0]
        out_elem = port_ops[op_name]['out'][body.attrib['parts']]

        in_headers = []
        out_headers = []

        # get the header too, but there are multiple elements
        for header in op_input.findall('soap:header', ns):
            in_headers.append(port_ops[op_name]['in'][header.attrib['part']])

        for header in op_output.findall('soap:header', ns):
            out_headers.append(port_ops[op_name]['out'][header.attrib['part']])

        operations[op_name] = SoapOperation(op_name, in_elem, out_elem,
                                            in_headers, out_headers)


def create_header_types(operations, types, elements):

    # define JsonRequestHeaders and JsonResponseHeaders
    # -> OWA doesn't seem to treat them specially, so merge them all for simplicity
    request_type = TypeData(None, "JsonRequestHeaders", None, False)
    response_type = TypeData(None, "JsonResponseHeaders", None, False)

    for op in operations.values():
        for hdr in op.in_headers:
            request_type.elements[hdr] = elements[hdr]

        for hdr in op.out_headers:
            response_type.elements[hdr] = elements[hdr]

    types["JsonRequestHeaders"] = request_type
    types["JsonResponseHeaders"] = response_type


golang_header = '''
//
// Code generated by ews_processor.py; DO NOT EDIT
//

package ews

import (
\t"github.com/virtuald/go-ordered-json"
)

func init() {
\tfor _, v := range(ewsTypes) {
\t\tv.Initialize()
\t}
}

var ewsTypes = map[string]*EwsType{
'''

golang_simple_type_map = {
    'boolean': 'T_BOOL',
    'decimal': 'T_NUM',
    'string': 'T_STR',
    'enum': 'T_ENUM',
    'list': 'T_LIST'
}

golang_schema_fmt = '''\t"%(typename)s": {
\t\tName: "%(typename)s", JsonType: "%(jsontype)s",
\t\telements: []element{%(elements)s},%(jsonextra)s
\t\tAttributes: []element{%(attrs)s},
\t\tIsList: %(islist)s, IsSimple: %(simple)s,%(simple_type)s
\t\tAnyAttr: %(any)s, TextAttr: "%(textattr)s",%(json_list_name)s
\t\tEnumValues: []string{%(enum_values)s},
\t\tListItemTypeStr: "%(list_item_type)s",
\t},
'''

golang_op_fmt = '''\t"%(opname)s": {
\t\tAction: "%(action)s",
\t\tBodyType: "%(action)sRequest:#Exchange",
\t\tRequestType: "%(action)sJsonRequest:#Exchange", Request: ewsTypes["%(in_type)s"],
\t\tResponse: EwsJsonElement{JsonName: "%(rname)s", SingleType: NewEwsJsonType("%(out_elem)s", ewsTypes["%(out_type)s"])},
\t},
'''

golang_footer = '''

// separate so we don't have to do an additional map lookup on each request
var EwsSoapRequestHeader = ewsTypes["JsonRequestHeaders"]
var EwsSoapResponseHeader = EwsJsonElement{
\tJsonName: "Header",
\tSingleType: NewEwsJsonType("soap:Header", ewsTypes["JsonResponseHeaders"]),
}

'''

def generate_golang(elements, types, operations, fp):

    print(golang_header, file=fp)

    for typename in sorted(types):
        typ = types[typename]

        jsontype = ''

        # only define json type for non anonymous types
        if not typ.name.endswith('AnonType'):
            # allow override of json types
            if typ.json_name:
                jsontype = typ.json_name
            else:
                jsontype = typ.name
                if jsontype.endswith('Type'):
                    jsontype = jsontype[:-4]
            jsontype += ":#Exchange"

        attrs = ""
        if typ.attrs:
            aa = []
            for n, v in sorted(typ.attrs.items()):
                t = ''
                if v:
                    t = ', T: "%s"' % v.name

                aa.append('{XN: "%s"%s},' % (n, t))

            attrs = '\n\t\t\t' + '\n\t\t\t'.join(aa) + '\n\t\t'

        elems = ""
        if typ.elements:
            ee = []
            for k,v in typ.elements.items():
                jname = ''
                jhint = ''
                if v.xml_name:
                    ename = shorten_qname(v.xml_name)
                else:
                    ename = shorten_qname(k)
                is_list = ''
                json_default = ''

                if not v.type:
                    raise ValueError("Should not happen anymore: %s // %s" % (typename, k))

                if v.is_list:
                    is_list = ', List: true'

                if v.json_name:
                    jname = ', JN: "%s"' % v.json_name
                
                if v.json_hint:
                    jhint = ', JT: "%s"' % v.json_hint

                if v.json_default:
                    json_default = ', JsonDefault: %s' % v.json_default

                ee.append('{XN: "%s"%s, T: "%s"%s%s%s},' % (ename, jname, v.type.name, is_list, json_default, jhint))

            elems = '\n\t\t\t' + '\n\t\t\t'.join(ee) + '\n\t\t'

        json_extra = ''
        if typ.json_extra:
            json_extra = '\n\t\t\t\tJsonExtra: []string{"%s"},' % '", "'.join(typ.json_extra)

        json_list_name = ''
        if typ.json_list_name:
            json_list_name = '\n\t\tJsonListName: "%s",' % typ.json_list_name

        simple_type = ''
        if typ.simple_type:
            simple_type = ' SimpleType: %s,' % golang_simple_type_map[typ.simple_type]

        enum_values = ", ".join('"{0}"'.format(value) for value in typ.enum_values)


        print(golang_schema_fmt % dict(
            typename=typ.name, jsontype=jsontype,
            elements=elems, attrs=attrs,
            any="true" if typ.any_attr else "false",
            simple="true" if typ.simple_type else "false",
            simple_type=simple_type,
            islist="true" if typ.is_list else "false",
            textattr="" if not typ.json_text_attr else typ.json_text_attr,
            jsonextra=json_extra, json_list_name=json_list_name,
            enum_values=enum_values,
            list_item_type="" if not typ.list_item_type else typ.list_item_type
        ), file=fp)

    print("}\n", file=fp)

    # generate xml message lookups
    # -> indexed by input action name
    print('var EwsOperations = map[string]*OpDescriptor{', file=fp)

    for opname in sorted(operations):
        op = operations[opname]
        in_type = elements[op.in_elem].type.name
        out_type = elements[op.out_elem].type.name

        print(golang_op_fmt % dict(
            opname=opname,
            action=op.action,
            in_type=in_type, out_type=out_type,
            out_elem=shorten_qname(op.out_elem),
            rname=split_qname(op.out_elem)[1],
        ), file=fp)

    print("}\n", file=fp)

    # generate a lookup to go the other way JSON -> XML

    # and the footer
    print(golang_footer, file=fp)


if __name__ == '__main__':

    if len(sys.argv) < 2:
        print("Usage: %s outfile" % sys.argv[0])
        exit(1)

    outfile = sys.argv[1]

    elements = {}
    types = {}
    operations = {}
    cls_hierarchy = {}

    thisdir = abspath(dirname(__file__))

    process_builtins(types, cls_hierarchy)

    process_schema(join(thisdir, 'types.xsd'), elements, types, cls_hierarchy)
    process_schema(join(thisdir, 'messages.xsd'), elements, types, cls_hierarchy)

    for v in types.values():
        v.finish()

    process_wsdl(join(thisdir, 'services.wsdl'), operations)

    # do this separately in case we want to reuse this in
    # the future...
    create_header_types(operations, types, elements)

    # manual adjustments to schemas when we just can't make sense of it all
    apply_hacks(operations, types, elements)

    with open(outfile, 'w') as fp:
        generate_golang(elements, types, operations, fp)
