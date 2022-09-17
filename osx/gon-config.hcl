source = ["./Vice.app"]
bundle_id = "org.pharr.vice"

apple_id {
  password = "@env:APPLE_CODESIGN_PASSWORD"
}

sign {
  application_identity = "Developer ID Application: Matthew Pharr"
}

dmg {
  output_path = "Vice.dmg"
  volume_name = "Vice"
}
