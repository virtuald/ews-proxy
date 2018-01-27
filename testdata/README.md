Test Data
=========

This is a collection of testdata to ensure that our XML <-> JSON translation
engine works correctly (and doesn't break with future improvements).

Requests directory:

* `*.xml` files are input SOAP files from davmail, macmail, or some other EWS
  speaking client
* `*.xml.json` files are what should be sent to the exchange server as a result

Responses directory:

* `*.json` are responses sent from exchange back to the EWS client (ideally,
  these are anonymized responses actually sent from exchange)
* `*.json.xml` are what should be sent to davmail, macmail, etc
* the prefix of the filename up to the first underscore MUST be the name of the
  action to be executed.

As we find cases where the translator fails, we should add more test cases.
Critical to this is providing an easy way for users to provide test data when
failures occur.

DAVMail Compatibility
=====================

Here's an initial list of the EWS operations we need to support for the current
version of DAVMail:

* CopyItem
* CreateFolder
* CreateAttachment
* DeleteAttachment
* DeleteFolder
* DeleteItem
* FindFolder
* FindItem
* GetAttachment
* GetFolder
* GetItem
* GetUserAvailability
* GetUserConfiguration
* MoveItem
* MoveFolder
* ResolveNames?
* UpdateFolder
* UpdateItem

OSX Mail Compatibility
======================

It's not currently known what APIs OSX Mail uses.
