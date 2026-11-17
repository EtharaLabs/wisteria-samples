import json
path = "/opt/rewardkit/site/litellm/model_prices_and_context_window_backup.json"
with open(path, encoding="utf-8") as f:
    data = json.load(f)
entry = data["claude-sonnet-5-5"]
assert entry.get("supports_native_structured_output") is True, entry.get("supports_native_structured_output")
entry["supports_native_structured_output"] = False
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=4)
    f.write("\n")
with open(path, encoding="utf-8") as f:
    assert json.load(f)["claude-sonnet-5-5"]["supports_native_structured_output"] is False
print("litellm model map: claude-sonnet-5-5 supports_native_structured_output set to false")
