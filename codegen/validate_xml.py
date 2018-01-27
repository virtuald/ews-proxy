#!/usr/bin/env python

from __future__ import print_function

import os
from os.path import abspath, dirname, join
import sys
import traceback
from lxml import etree

# Validates the body of EWS XML files against the appropriate schemas.

root = abspath(dirname(__file__))
schema_doc = etree.parse(join(root, 'messages.xsd'))
my_schema = etree.XMLSchema(schema_doc)

ns = {'Soap': 'http://schemas.xmlsoap.org/soap/envelope/',
      'm': 'http://schemas.microsoft.com/exchange/services/2006/messages',
      't': 'http://schemas.microsoft.com/exchange/services/2006/types'}


def validate_file(file_name):

    try:
        tree = etree.parse(file_name)

        body = tree.find('Soap:Body', ns)

        # Validate the elements contained in the Body of the XML file
        # This is necessary to strip the SOAP elements
        for child in body:
            try:
                my_schema.assertValid(child)
            except etree.DocumentInvalid as e:
                print (file_name + " : FAILED at " + str(e))
                return False
            except Exception as e:
                print (e)
                return False

        return True
    except Exception as e:
        print(file_name + " could not be parsed, see error: " + e.message)
        traceback.print_exc(e)
        return False

if __name__ == '__main__':
    
    if len(sys.argv) != 2:
        print("Usage: validate_xml.py directory")
        exit(1)
    
    folder = sys.argv[1]
    xml_files = []
    xml_files += [each for each in os.listdir(folder) if each.endswith('.xml')]

    success_count = 0
    fail_count = 0
    total_count = xml_files.__len__()
    for xml_file in xml_files:
        if validate_file(folder + "/" + xml_file):
            success_count += 1
            print(xml_file + " : VALIDATED")
        else:
            fail_count += 1

    print(str(success_count) + " files passed out of " + str(total_count) + " files in " + folder)
    
    exit(1 if fail_count else 1)
