# Import by TYPE/name:
terraform import dreamrouter_dns_record.nas A/nas.home.internal

# When several records share a type and name (e.g. round-robin A records),
# add the value to pick one:
terraform import dreamrouter_dns_record.rr A/rr.home.internal/192.168.1.11
