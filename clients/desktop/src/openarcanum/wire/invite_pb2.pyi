from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable
from typing import ClassVar as _ClassVar, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class Invite(_message.Message):
    __slots__ = ("route_id", "owner_key", "server_key", "relays", "join_key", "name")
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    RELAYS_FIELD_NUMBER: _ClassVar[int]
    JOIN_KEY_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    route_id: bytes
    owner_key: bytes
    server_key: bytes
    relays: _containers.RepeatedScalarFieldContainer[str]
    join_key: str
    name: str
    def __init__(self, route_id: _Optional[bytes] = ..., owner_key: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., relays: _Optional[_Iterable[str]] = ..., join_key: _Optional[str] = ..., name: _Optional[str] = ...) -> None: ...

class ClaimCode(_message.Message):
    __slots__ = ("server_key", "route_id", "relays", "secret")
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    RELAYS_FIELD_NUMBER: _ClassVar[int]
    SECRET_FIELD_NUMBER: _ClassVar[int]
    server_key: bytes
    route_id: bytes
    relays: _containers.RepeatedScalarFieldContainer[str]
    secret: bytes
    def __init__(self, server_key: _Optional[bytes] = ..., route_id: _Optional[bytes] = ..., relays: _Optional[_Iterable[str]] = ..., secret: _Optional[bytes] = ...) -> None: ...

class MoveCode(_message.Message):
    __slots__ = ("server_key", "route_id", "relays", "secret", "owner_key")
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    RELAYS_FIELD_NUMBER: _ClassVar[int]
    SECRET_FIELD_NUMBER: _ClassVar[int]
    OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    server_key: bytes
    route_id: bytes
    relays: _containers.RepeatedScalarFieldContainer[str]
    secret: bytes
    owner_key: bytes
    def __init__(self, server_key: _Optional[bytes] = ..., route_id: _Optional[bytes] = ..., relays: _Optional[_Iterable[str]] = ..., secret: _Optional[bytes] = ..., owner_key: _Optional[bytes] = ...) -> None: ...
