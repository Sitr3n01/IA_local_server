// Minimal UnityEngine surface. Enough to type-check a MonoBehaviour without a
// Unity install; deliberately NOT a Unity emulator.
namespace UnityEngine
{
    public class Object { public string name; public static void Destroy(Object o) { } }
    public class Component : Object { public GameObject gameObject; public Transform transform; }
    public class Behaviour : Component { public bool enabled; }
    public class Transform : Component { public Vector3 position; public void SetParent(Transform p) { } }
    public class GameObject : Object
    {
        public GameObject() { }
        public GameObject(string n) { name = n; }
        public Transform transform { get; } = new Transform();
        public bool activeSelf { get; private set; }
        public void SetActive(bool v) { activeSelf = v; }
        public T GetComponent<T>() where T : Component => null;
        public T AddComponent<T>() where T : Component, new() => new T();
    }
    public struct Vector3
    {
        public float x, y, z;
        public Vector3(float x, float y, float z) { this.x = x; this.y = y; this.z = z; }
        public static Vector3 zero => new Vector3(0, 0, 0);
    }
    public class MonoBehaviour : Behaviour
    {
        public static T Instantiate<T>(T original) where T : Object => original;
        public static T Instantiate<T>(T original, Transform parent) where T : Object => original;
    }
    public class ScriptableObject : Object { }
    public static class Debug
    {
        public static void Log(object m) { }
        public static void LogWarning(object m) { }
        public static void LogError(object m) { }
    }
    public class SerializeFieldAttribute : System.Attribute { }
    public class RequireComponent : System.Attribute { public RequireComponent(System.Type t) { } }
}
