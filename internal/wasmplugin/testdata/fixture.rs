// A real ABI v2 plugin used by host, service and MCP integration tests.
// No third-party crates. Build with build-fixture.sh (Rust is not needed to run tests).
#[link(wasm_import_module = "sourcegraph")]
extern "C" {
    fn read_file(ptr: u32, len: u32) -> u64;
    fn read_files(ptr: u32, len: u32) -> u64;
    fn last_error() -> u64;
}
#[used]
#[link_section = "sourcegraph.plugin.v1"]
static METADATA: [u8; 60] = *b"{\"name\":\"fixture\",\"abi_version\":2,\"title\":\"ABI test plugin\"}";
static mut CALLS: u32 = 0;
#[no_mangle]
pub extern "C" fn alloc(len: u32) -> u32 {
    let mut v = vec![0u8; len as usize].into_boxed_slice();
    let ptr = v.as_mut_ptr(); std::mem::forget(v); ptr as u32
}
#[no_mangle]
pub unsafe extern "C" fn dealloc(ptr: u32, len: u32) {
    if ptr != 0 { drop(Box::from_raw(std::ptr::slice_from_raw_parts_mut(ptr as *mut u8, len as usize))); }
}
fn output(bytes: &[u8]) -> u64 {
    let p = alloc(bytes.len() as u32);
    unsafe { std::ptr::copy_nonoverlapping(bytes.as_ptr(), p as *mut u8, bytes.len()); }
    ((p as u64) << 32) | bytes.len() as u64
}
#[no_mangle]
pub unsafe extern "C" fn enrich(ptr: u32, len: u32) -> u64 {
    let input = String::from_utf8_lossy(std::slice::from_raw_parts(ptr as *const u8, len as usize)).to_string();
    if input.contains("\"action\":\"loop\"") { loop { std::hint::spin_loop(); } }
    if input.contains("\"action\":\"trap\"") { core::arch::wasm32::unreachable(); }
    if input.contains("\"action\":\"badptr\"") { return (0xfffffff0u64 << 32) | 100; }
    if input.contains("\"action\":\"invalid\"") { return output(b"not json"); }
    if input.contains("\"action\":\"scalar\"") { return output(b"42"); }
    if input.contains("\"action\":\"counter\"") { CALLS+=1; let calls=CALLS; return output(calls.to_string().as_bytes()); }
    if input.contains("\"action\":\"batch\"") {
        let paths = b"[\"fixture.json\",\"missing.json\"]";
        let packed=read_files(paths.as_ptr() as u32,paths.len() as u32);
        if packed == 0 { let error=last_error(); dealloc((error>>32) as u32,error as u32); return output(b"false"); }
        let b=std::slice::from_raw_parts((packed>>32) as *const u8,packed as u32 as usize);
        let ok = u32::from_le_bytes(b[0..4].try_into().unwrap())==2 && u32::from_le_bytes(b[4..8].try_into().unwrap())==0;
        dealloc((packed>>32) as u32,packed as u32);
        return output(if ok {b"true"}else{b"false"});
    }
    if input.contains("\"action\":\"read\"") {
        let path=b"fixture.json";
        let packed=read_file(path.as_ptr() as u32,path.len() as u32);
        if packed!=0 {return packed;}
        let error=last_error(); dealloc((error>>32) as u32,error as u32);
        return output(b"{\"issues\":[{\"code\":\"FIXTURE_READ_FAILED\"}]}");
    }
    if input.contains("\"action\":\"escape\"") {
        let path=b"../secret.json";
        let packed=read_file(path.as_ptr() as u32,path.len() as u32);
        if packed!=0 {return packed;}
        return output(b"{\"denied\":true}");
    }
    if input.contains("\"action\":\"attachments\"") {
        let mut items = Vec::new();
        for tail in input.split("\"item_id\":\"").skip(1) {
            let id=tail.split('"').next().unwrap();
            items.push(format!("{{\"item_id\":\"{}\",\"text\":\"references: fixture-evidence\"}}",id));
        }
        return output(format!("{{\"attachments\":[{}]}}",items.join(",")).as_bytes());
    }
    output(input.as_bytes())
}
