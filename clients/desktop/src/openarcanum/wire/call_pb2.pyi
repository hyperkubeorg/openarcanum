from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class CallGrant(_message.Message):
    __slots__ = ("call_id", "server_key", "call_nonce", "participant", "kid", "audio", "video", "screen", "priority", "expires_at", "max_participants")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    CALL_NONCE_FIELD_NUMBER: _ClassVar[int]
    PARTICIPANT_FIELD_NUMBER: _ClassVar[int]
    KID_FIELD_NUMBER: _ClassVar[int]
    AUDIO_FIELD_NUMBER: _ClassVar[int]
    VIDEO_FIELD_NUMBER: _ClassVar[int]
    SCREEN_FIELD_NUMBER: _ClassVar[int]
    PRIORITY_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    MAX_PARTICIPANTS_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    server_key: bytes
    call_nonce: bytes
    participant: bytes
    kid: int
    audio: bool
    video: bool
    screen: bool
    priority: bool
    expires_at: int
    max_participants: int
    def __init__(self, call_id: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., call_nonce: _Optional[bytes] = ..., participant: _Optional[bytes] = ..., kid: _Optional[int] = ..., audio: _Optional[bool] = ..., video: _Optional[bool] = ..., screen: _Optional[bool] = ..., priority: _Optional[bool] = ..., expires_at: _Optional[int] = ..., max_participants: _Optional[int] = ...) -> None: ...

class SignedCallGrant(_message.Message):
    __slots__ = ("grant", "signature")
    GRANT_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    grant: bytes
    signature: bytes
    def __init__(self, grant: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class CallControl(_message.Message):
    __slots__ = ("call_id", "seq", "muted", "deafened", "removed", "at", "no_video", "no_screen")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    MUTED_FIELD_NUMBER: _ClassVar[int]
    DEAFENED_FIELD_NUMBER: _ClassVar[int]
    REMOVED_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    NO_VIDEO_FIELD_NUMBER: _ClassVar[int]
    NO_SCREEN_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    seq: int
    muted: _containers.RepeatedScalarFieldContainer[bytes]
    deafened: _containers.RepeatedScalarFieldContainer[bytes]
    removed: _containers.RepeatedScalarFieldContainer[bytes]
    at: int
    no_video: _containers.RepeatedScalarFieldContainer[bytes]
    no_screen: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, call_id: _Optional[bytes] = ..., seq: _Optional[int] = ..., muted: _Optional[_Iterable[bytes]] = ..., deafened: _Optional[_Iterable[bytes]] = ..., removed: _Optional[_Iterable[bytes]] = ..., at: _Optional[int] = ..., no_video: _Optional[_Iterable[bytes]] = ..., no_screen: _Optional[_Iterable[bytes]] = ...) -> None: ...

class SignedCallControl(_message.Message):
    __slots__ = ("control", "signature")
    CONTROL_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    control: bytes
    signature: bytes
    def __init__(self, control: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class CallJoin(_message.Message):
    __slots__ = ("grant", "offer", "req", "kinds", "control")
    class KindsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    GRANT_FIELD_NUMBER: _ClassVar[int]
    OFFER_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    KINDS_FIELD_NUMBER: _ClassVar[int]
    CONTROL_FIELD_NUMBER: _ClassVar[int]
    grant: SignedCallGrant
    offer: str
    req: int
    kinds: _containers.ScalarMap[str, str]
    control: SignedCallControl
    def __init__(self, grant: _Optional[_Union[SignedCallGrant, _Mapping]] = ..., offer: _Optional[str] = ..., req: _Optional[int] = ..., kinds: _Optional[_Mapping[str, str]] = ..., control: _Optional[_Union[SignedCallControl, _Mapping]] = ...) -> None: ...

class CallAnswer(_message.Message):
    __slots__ = ("call_id", "answer", "req")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    ANSWER_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    answer: str
    req: int
    def __init__(self, call_id: _Optional[bytes] = ..., answer: _Optional[str] = ..., req: _Optional[int] = ...) -> None: ...

class CallSignal(_message.Message):
    __slots__ = ("call_id", "type", "sdp")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    SDP_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    type: str
    sdp: str
    def __init__(self, call_id: _Optional[bytes] = ..., type: _Optional[str] = ..., sdp: _Optional[str] = ...) -> None: ...

class CallLeave(_message.Message):
    __slots__ = ("call_id",)
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    def __init__(self, call_id: _Optional[bytes] = ...) -> None: ...

class CallLayer(_message.Message):
    __slots__ = ("call_id", "source", "kind", "rid")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    RID_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    source: bytes
    kind: str
    rid: str
    def __init__(self, call_id: _Optional[bytes] = ..., source: _Optional[bytes] = ..., kind: _Optional[str] = ..., rid: _Optional[str] = ...) -> None: ...

class CallListen(_message.Message):
    __slots__ = ("call_id", "deaf", "no_audio", "screen_audio")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    DEAF_FIELD_NUMBER: _ClassVar[int]
    NO_AUDIO_FIELD_NUMBER: _ClassVar[int]
    SCREEN_AUDIO_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    deaf: bool
    no_audio: _containers.RepeatedScalarFieldContainer[bytes]
    screen_audio: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, call_id: _Optional[bytes] = ..., deaf: _Optional[bool] = ..., no_audio: _Optional[_Iterable[bytes]] = ..., screen_audio: _Optional[_Iterable[bytes]] = ...) -> None: ...

class CallWatchers(_message.Message):
    __slots__ = ("call_id", "watches")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    WATCHES_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    watches: _containers.RepeatedCompositeFieldContainer[CallWatch]
    def __init__(self, call_id: _Optional[bytes] = ..., watches: _Optional[_Iterable[_Union[CallWatch, _Mapping]]] = ...) -> None: ...

class CallWatch(_message.Message):
    __slots__ = ("kind", "receivers", "layers")
    KIND_FIELD_NUMBER: _ClassVar[int]
    RECEIVERS_FIELD_NUMBER: _ClassVar[int]
    LAYERS_FIELD_NUMBER: _ClassVar[int]
    kind: str
    receivers: int
    layers: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, kind: _Optional[str] = ..., receivers: _Optional[int] = ..., layers: _Optional[_Iterable[str]] = ...) -> None: ...

class CallUpdate(_message.Message):
    __slots__ = ("control",)
    CONTROL_FIELD_NUMBER: _ClassVar[int]
    control: SignedCallControl
    def __init__(self, control: _Optional[_Union[SignedCallControl, _Mapping]] = ...) -> None: ...

class CallState(_message.Message):
    __slots__ = ("call_id", "members", "ended")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    ENDED_FIELD_NUMBER: _ClassVar[int]
    call_id: bytes
    members: _containers.RepeatedCompositeFieldContainer[CallMember]
    ended: str
    def __init__(self, call_id: _Optional[bytes] = ..., members: _Optional[_Iterable[_Union[CallMember, _Mapping]]] = ..., ended: _Optional[str] = ...) -> None: ...

class CallMember(_message.Message):
    __slots__ = ("participant", "kid", "audio", "camera", "screen", "muted", "deafened", "priority")
    PARTICIPANT_FIELD_NUMBER: _ClassVar[int]
    KID_FIELD_NUMBER: _ClassVar[int]
    AUDIO_FIELD_NUMBER: _ClassVar[int]
    CAMERA_FIELD_NUMBER: _ClassVar[int]
    SCREEN_FIELD_NUMBER: _ClassVar[int]
    MUTED_FIELD_NUMBER: _ClassVar[int]
    DEAFENED_FIELD_NUMBER: _ClassVar[int]
    PRIORITY_FIELD_NUMBER: _ClassVar[int]
    participant: bytes
    kid: int
    audio: bool
    camera: bool
    screen: bool
    muted: bool
    deafened: bool
    priority: bool
    def __init__(self, participant: _Optional[bytes] = ..., kid: _Optional[int] = ..., audio: _Optional[bool] = ..., camera: _Optional[bool] = ..., screen: _Optional[bool] = ..., muted: _Optional[bool] = ..., deafened: _Optional[bool] = ..., priority: _Optional[bool] = ...) -> None: ...
