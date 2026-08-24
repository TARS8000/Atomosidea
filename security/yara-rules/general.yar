rule example_malware_string {
  strings:
    $a = "This is a malicious string" ascii wide
    $b = "evil_payload.js" ascii wide
  condition:
    $a or $b
}

rule html_obfuscation {
  strings:
    $s1 = "eval(function(p,a,c,k,e,d)" ascii wide
    $s2 = "document.write(unescape(" ascii wide
    $s3 = "<iframe src=\"data:text/html;base64," ascii wide
  condition:
    any of them
}

rule unity_webgl_suspicious_api {
  strings:
    $s1 = "WebGL.framework.js" ascii wide
    $s2 = "Module._main" ascii wide
    $s3 = "window.open('http" ascii wide
    $s4 = "fetch('http" ascii wide
  condition:
    ($s1 or $s2) and (2 of ($s3, $s4))
}
