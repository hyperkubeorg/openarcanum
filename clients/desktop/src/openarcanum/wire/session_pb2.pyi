from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ClientHello(_message.Message):
    __slots__ = ("version", "versions")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    version: int
    versions: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, version: _Optional[int] = ..., versions: _Optional[_Iterable[int]] = ...) -> None: ...

class ServerIdentity(_message.Message):
    __slots__ = ("server_key", "static_sig", "cert", "version", "versions")
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    STATIC_SIG_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    server_key: bytes
    static_sig: bytes
    cert: SignedServerCert
    version: int
    versions: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, server_key: _Optional[bytes] = ..., static_sig: _Optional[bytes] = ..., cert: _Optional[_Union[SignedServerCert, _Mapping]] = ..., version: _Optional[int] = ..., versions: _Optional[_Iterable[int]] = ...) -> None: ...

class ClientIdentity(_message.Message):
    __slots__ = ("identity_key", "static_sig", "device_name")
    IDENTITY_KEY_FIELD_NUMBER: _ClassVar[int]
    STATIC_SIG_FIELD_NUMBER: _ClassVar[int]
    DEVICE_NAME_FIELD_NUMBER: _ClassVar[int]
    identity_key: bytes
    static_sig: bytes
    device_name: str
    def __init__(self, identity_key: _Optional[bytes] = ..., static_sig: _Optional[bytes] = ..., device_name: _Optional[str] = ...) -> None: ...

class ServerCert(_message.Message):
    __slots__ = ("owner_key", "server_key", "issued_at", "seq", "community", "owner_seq", "owner_head")
    OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    ISSUED_AT_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    OWNER_SEQ_FIELD_NUMBER: _ClassVar[int]
    OWNER_HEAD_FIELD_NUMBER: _ClassVar[int]
    owner_key: bytes
    server_key: bytes
    issued_at: int
    seq: int
    community: bytes
    owner_seq: int
    owner_head: bytes
    def __init__(self, owner_key: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., issued_at: _Optional[int] = ..., seq: _Optional[int] = ..., community: _Optional[bytes] = ..., owner_seq: _Optional[int] = ..., owner_head: _Optional[bytes] = ...) -> None: ...

class SignedServerCert(_message.Message):
    __slots__ = ("cert", "signature", "owner_chain")
    CERT_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    OWNER_CHAIN_FIELD_NUMBER: _ClassVar[int]
    cert: bytes
    signature: bytes
    owner_chain: _containers.RepeatedCompositeFieldContainer[SignedOwnerChange]
    def __init__(self, cert: _Optional[bytes] = ..., signature: _Optional[bytes] = ..., owner_chain: _Optional[_Iterable[_Union[SignedOwnerChange, _Mapping]]] = ...) -> None: ...

class OwnerKeyRequest(_message.Message):
    __slots__ = ("community", "owner_key", "member")
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    community: bytes
    owner_key: bytes
    member: bytes
    def __init__(self, community: _Optional[bytes] = ..., owner_key: _Optional[bytes] = ..., member: _Optional[bytes] = ...) -> None: ...

class OwnerAdd(_message.Message):
    __slots__ = ("request", "owner_sig", "member_sig")
    REQUEST_FIELD_NUMBER: _ClassVar[int]
    OWNER_SIG_FIELD_NUMBER: _ClassVar[int]
    MEMBER_SIG_FIELD_NUMBER: _ClassVar[int]
    request: bytes
    owner_sig: bytes
    member_sig: bytes
    def __init__(self, request: _Optional[bytes] = ..., owner_sig: _Optional[bytes] = ..., member_sig: _Optional[bytes] = ...) -> None: ...

class OwnerChange(_message.Message):
    __slots__ = ("community", "seq", "prev", "signer", "at", "add", "remove")
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    PREV_FIELD_NUMBER: _ClassVar[int]
    SIGNER_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    ADD_FIELD_NUMBER: _ClassVar[int]
    REMOVE_FIELD_NUMBER: _ClassVar[int]
    community: bytes
    seq: int
    prev: bytes
    signer: bytes
    at: int
    add: OwnerAdd
    remove: bytes
    def __init__(self, community: _Optional[bytes] = ..., seq: _Optional[int] = ..., prev: _Optional[bytes] = ..., signer: _Optional[bytes] = ..., at: _Optional[int] = ..., add: _Optional[_Union[OwnerAdd, _Mapping]] = ..., remove: _Optional[bytes] = ...) -> None: ...

class SignedOwnerChange(_message.Message):
    __slots__ = ("change", "signature")
    CHANGE_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    change: bytes
    signature: bytes
    def __init__(self, change: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...
