// Compiles the domain's descriptor set (w17/services/notes-business/
// descriptors.binpb, found through the manifest beside it) into the tonic
// services and messages src/main.rs implements. No protoc: the descriptor
// set is already compiled.
fn main() -> Result<(), Box<dyn std::error::Error>> {
    w17_business_build::compile()
}
