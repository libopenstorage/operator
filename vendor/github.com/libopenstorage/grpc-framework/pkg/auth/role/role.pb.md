# gRPC API Reference

## Contents



- Messages
    - [Role](#role)
    - [Rule](#rule)
  



- [Scalar Value Types](#scalar-value-types)



 <!-- end services -->

## Messages


### Role {#role}



| Field | Type | Description |
| ----- | ---- | ----------- |
| name | [ string](#string) | none |
| rules | [repeated Rule](#rule) | none |
 <!-- end Fields -->
 <!-- end HasFields -->


### Rule {#rule}
Rule is the message used to construct custom RBAC roles

#### Format
The following shows the supported format for Rule:

* Services: Is the gRPC service name in `[tag]<service name>` in lowercase
* Apis: Is the API name in the service in lowercase

Values can also be set to `*`, or start or end with `*` to allow multiple matches in services or apis.

Services and APIs can also be denied by prefixing the value with a `!`. Note that on rule conflicts,
denial will always be chosen.

#### Examples

* Allow any call:

```yaml
Rule:
  - Services: ["*"]
    Apis: ["*"]
```

* Allow only cluster operations:

```yaml
Rule:
  - services: ["cluster"]
    apis: ["*"]
```

* Allow inspection of any object and listings of only volumes

```yaml
Rule:
  - Services: ["volumes"]
    Apis: ["*enumerate*"]
  - Services: ["*"]
    Apis: ["inspect*"]
```

* Allow all volume call except create

```yaml
Rule:
  - Services: ["volumes"]
    Apis: ["*", "!create"]
```


| Field | Type | Description |
| ----- | ---- | ----------- |
| services | [repeated string](#string) | The gRPC service name in `[tag]<service name>` in lowercase |
| apis | [repeated string](#string) | The API name in the service in lowercase |
 <!-- end Fields -->
 <!-- end HasFields -->
 <!-- end messages -->

## Enums
 <!-- end Enums -->
 <!-- end Files -->

## Scalar Value Types

| .proto Type | Notes | C++ Type | Java Type | Python Type |
| ----------- | ----- | -------- | --------- | ----------- |
| <div><h4 id="double" /></div><a name="double" /> double |  | double | double | float |
| <div><h4 id="float" /></div><a name="float" /> float |  | float | float | float |
| <div><h4 id="int32" /></div><a name="int32" /> int32 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint32 instead. | int32 | int | int |
| <div><h4 id="int64" /></div><a name="int64" /> int64 | Uses variable-length encoding. Inefficient for encoding negative numbers – if your field is likely to have negative values, use sint64 instead. | int64 | long | int/long |
| <div><h4 id="uint32" /></div><a name="uint32" /> uint32 | Uses variable-length encoding. | uint32 | int | int/long |
| <div><h4 id="uint64" /></div><a name="uint64" /> uint64 | Uses variable-length encoding. | uint64 | long | int/long |
| <div><h4 id="sint32" /></div><a name="sint32" /> sint32 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int32s. | int32 | int | int |
| <div><h4 id="sint64" /></div><a name="sint64" /> sint64 | Uses variable-length encoding. Signed int value. These more efficiently encode negative numbers than regular int64s. | int64 | long | int/long |
| <div><h4 id="fixed32" /></div><a name="fixed32" /> fixed32 | Always four bytes. More efficient than uint32 if values are often greater than 2^28. | uint32 | int | int |
| <div><h4 id="fixed64" /></div><a name="fixed64" /> fixed64 | Always eight bytes. More efficient than uint64 if values are often greater than 2^56. | uint64 | long | int/long |
| <div><h4 id="sfixed32" /></div><a name="sfixed32" /> sfixed32 | Always four bytes. | int32 | int | int |
| <div><h4 id="sfixed64" /></div><a name="sfixed64" /> sfixed64 | Always eight bytes. | int64 | long | int/long |
| <div><h4 id="bool" /></div><a name="bool" /> bool |  | bool | boolean | boolean |
| <div><h4 id="string" /></div><a name="string" /> string | A string must always contain UTF-8 encoded or 7-bit ASCII text. | string | String | str/unicode |
| <div><h4 id="bytes" /></div><a name="bytes" /> bytes | May contain any arbitrary sequence of bytes. | string | ByteString | str |

