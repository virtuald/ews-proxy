package ews

// this doesn't actually close the window unless it's a popup window
var closePageHtml = `
<html>
  <head><title>Successful OWA login</title></head>
  <script type='text/javascript'>
    window.close();
  </script>
  <body>
    <p>Login to Exchange successful!</p>
  </body>
</html>
`

var keepAliveJsonAction = "GetFolder"
var keepAliveJson = []byte(`{
    "__type": "GetFolderJsonRequest:#Exchange",
    "Header": {
        "__type": "JsonRequestHeaders:#Exchange",
        "RequestServerVersion": "Exchange2013_SP1"
    },
    "Body": {
        "__type": "GetFolderRequest:#Exchange",
        "FolderShape": {
            "__type": "FolderResponseShape:#Exchange",
            "BaseShape": "IdOnly"
        },
        "FolderIds": [{
            "__type": "DistinguishedFolderId:#Exchange",
            "Id": "root"
        }]
    }
}`)
